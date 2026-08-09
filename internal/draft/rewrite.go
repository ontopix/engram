package draft

import (
	"bytes"
	"errors"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/ontopix/engram/internal/documentprofile"
	"github.com/ontopix/engram/internal/markdownprofile"
	"github.com/ontopix/engram/internal/snapshot"
	"github.com/ontopix/engram/internal/yamlprofile"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"
)

type byteReplacement struct {
	start int
	end   int
	value []byte
}

func rewriteMoveDocument(sourcePath, finalPath string, source []byte, recordFrontmatter bool, from, to string) ([]byte, bool, error) {
	document, err := documentprofile.Parse(source)
	if err != nil {
		return nil, false, fmt.Errorf("parse document while locating inbound links: %w", err)
	}
	fromTarget := strings.TrimSuffix(from, ".md")
	toTarget := strings.TrimSuffix(to, ".md")
	var replacements []byteReplacement

	for _, occurrence := range document.Markdown.Wikilinks {
		link, parseErr := documentprofile.ParseWikilink(occurrence.Raw)
		if parseErr != nil || link.Target != fromTarget {
			continue
		}
		span, ok := document.SourceSpan(occurrence.Span)
		if !ok {
			return nil, false, errors.New("wikilink source span is unavailable")
		}
		replacements = append(replacements, byteReplacement{
			start: span.Start + 2, end: span.Start + 2 + len(fromTarget), value: []byte(toTarget),
		})
	}

	if recordFrontmatter {
		frontmatter := document.FrontmatterBytes()
		for _, occurrence := range documentprofile.YAMLWikilinks(document.YAML.Root) {
			if occurrence.Err != nil || occurrence.Link.Target != fromTarget {
				continue
			}
			replacement, locateErr := frontmatterWikilinkReplacement(frontmatter, occurrence.Position, fromTarget, toTarget)
			if locateErr != nil {
				return nil, false, fmt.Errorf("frontmatter %s: %w", occurrence.Pointer, locateErr)
			}
			replacements = append(replacements, byteReplacement{
				start: document.Frontmatter.Start + replacement.start,
				end:   document.Frontmatter.Start + replacement.end,
				value: replacement.value,
			})
		}
	}

	markdownEdits, err := markdownMoveReplacements(document.BodyBytes(), sourcePath, finalPath, from, to)
	if err != nil {
		return nil, false, err
	}
	for _, edit := range markdownEdits {
		edit.start += document.Body.Start
		edit.end += document.Body.Start
		replacements = append(replacements, edit)
	}
	if len(replacements) == 0 {
		return append([]byte(nil), source...), false, nil
	}
	result, err := applyByteReplacements(source, replacements)
	if err != nil {
		return nil, false, err
	}
	parsed, err := documentprofile.Parse(result)
	if err != nil {
		return nil, false, fmt.Errorf("rewritten document is not parseable: %w", err)
	}
	if err := verifyMarkdownMove(document.Markdown, parsed.Markdown, sourcePath, finalPath, from, to); err != nil {
		return nil, false, err
	}
	for _, occurrence := range parsed.Markdown.Wikilinks {
		link, parseErr := documentprofile.ParseWikilink(occurrence.Raw)
		if parseErr == nil && link.Target == fromTarget {
			return nil, false, errors.New("unsupported or ambiguous body wikilink presentation")
		}
	}
	if recordFrontmatter {
		for _, occurrence := range documentprofile.YAMLWikilinks(parsed.YAML.Root) {
			if occurrence.Err == nil && occurrence.Link.Target == fromTarget {
				return nil, false, errors.New("unsupported or ambiguous frontmatter wikilink presentation")
			}
		}
	}
	return result, !bytes.Equal(result, source), nil
}

func verifyMarkdownMove(before, after markdownprofile.Document, sourcePath, finalPath, from, to string) error {
	if len(before.Links) != len(after.Links) {
		return errors.New("Markdown link structure changed during rewrite")
	}
	for index := range before.Links {
		original := before.Links[index]
		rewritten := after.Links[index]
		if original.Image != rewritten.Image {
			return errors.New("Markdown link/image structure changed during rewrite")
		}
		target, directoryOnly, external, valid := resolveLocalDestination(sourcePath, original.Destination)
		if external || !valid {
			if rewritten.Destination != original.Destination {
				return errors.New("unrelated Markdown destination changed during rewrite")
			}
			continue
		}
		if target == from && !directoryOnly {
			target = to
		}
		finalTarget, finalDirectory, finalExternal, finalValid := resolveLocalDestination(finalPath, rewritten.Destination)
		if finalExternal || !finalValid || finalTarget != target || finalDirectory != directoryOnly {
			return errors.New("rewritten Markdown destination does not preserve its meaning")
		}
	}
	return nil
}

func frontmatterWikilinkReplacement(source []byte, position yamlprofile.Position, fromTarget, toTarget string) (byteReplacement, error) {
	if position.Line < 1 || position.Column < 1 {
		return byteReplacement{}, errors.New("scalar source position is unavailable")
	}
	lineStart := 0
	for line := 1; line < position.Line; line++ {
		relative := bytes.IndexByte(source[lineStart:], '\n')
		if relative < 0 {
			return byteReplacement{}, errors.New("scalar line is outside frontmatter")
		}
		lineStart += relative + 1
	}
	positionOffset := lineStart
	for column := 1; column < position.Column; column++ {
		if positionOffset >= len(source) || source[positionOffset] == '\n' {
			return byteReplacement{}, errors.New("scalar column is outside frontmatter")
		}
		_, width := utf8.DecodeRune(source[positionOffset:])
		if width == 0 {
			return byteReplacement{}, errors.New("scalar column is unavailable")
		}
		positionOffset += width
	}
	lineEnd := len(source)
	if relative := bytes.IndexByte(source[positionOffset:], '\n'); relative >= 0 {
		lineEnd = positionOffset + relative
	}
	encodedFrom := fromTarget
	encodedTo := toTarget
	if positionOffset < len(source) && source[positionOffset] == '\'' {
		encodedFrom = strings.ReplaceAll(encodedFrom, "'", "''")
		encodedTo = strings.ReplaceAll(encodedTo, "'", "''")
	}
	if positionOffset < len(source) && (source[positionOffset] == '|' || source[positionOffset] == '>') {
		return byteReplacement{}, errors.New("unsupported block scalar presentation")
	}
	needle := []byte("[[" + encodedFrom)
	relative := bytes.Index(source[positionOffset:lineEnd], needle)
	if relative < 0 {
		return byteReplacement{}, errors.New("unsupported escaped or multiline scalar presentation")
	}
	start := positionOffset + relative + 2
	end := start + len(encodedFrom)
	if end >= lineEnd || source[end] != '|' && source[end] != ']' {
		return byteReplacement{}, errors.New("ambiguous scalar wikilink target presentation")
	}
	return byteReplacement{start: start, end: end, value: []byte(encodedTo)}, nil
}

func applyByteReplacements(source []byte, replacements []byteReplacement) ([]byte, error) {
	sort.Slice(replacements, func(i, j int) bool {
		if replacements[i].start != replacements[j].start {
			return replacements[i].start < replacements[j].start
		}
		return replacements[i].end < replacements[j].end
	})
	last := 0
	capacity := len(source)
	for _, replacement := range replacements {
		capacity += len(replacement.value) - (replacement.end - replacement.start)
	}
	result := make([]byte, 0, capacity)
	for _, replacement := range replacements {
		if replacement.start < last || replacement.start < 0 || replacement.end < replacement.start || replacement.end > len(source) {
			return nil, errors.New("overlapping or invalid link source spans")
		}
		result = append(result, source[last:replacement.start]...)
		result = append(result, replacement.value...)
		last = replacement.end
	}
	result = append(result, source[last:]...)
	return result, nil
}

type markdownLinkSpec struct {
	destination string
	image       bool
}

type markdownDestinationSpan struct {
	start       int
	end         int
	destination string
	image       bool
	angle       bool
}

func markdownMoveReplacements(source []byte, sourcePath, finalPath, from, to string) ([]byteReplacement, error) {
	markdown := goldmark.New(goldmark.WithParserOptions(parser.WithAutoHeadingID()))
	root := markdown.Parser().Parse(text.NewReader(source))
	var inline []markdownLinkSpec
	usedReferences := make(map[string]string)
	definitions := make(map[string]*ast.LinkReferenceDefinition)
	var definitionOrder []*ast.LinkReferenceDefinition
	var excluded []markdownprofile.Span

	_ = ast.Walk(root, func(node ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		switch typed := node.(type) {
		case *ast.Link:
			destination := commonMarkDestination(typed.Destination)
			if typed.Reference == nil {
				inline = append(inline, markdownLinkSpec{destination: destination})
			} else {
				usedReferences[util.ToLinkReference(typed.Reference.Value)] = destination
			}
		case *ast.Image:
			destination := commonMarkDestination(typed.Destination)
			if typed.Reference == nil {
				inline = append(inline, markdownLinkSpec{destination: destination, image: true})
			} else {
				usedReferences[util.ToLinkReference(typed.Reference.Value)] = destination
			}
		case *ast.LinkReferenceDefinition:
			key := util.ToLinkReference(typed.Label)
			if _, exists := definitions[key]; !exists {
				definitions[key] = typed
				definitionOrder = append(definitionOrder, typed)
			}
		case *ast.CodeSpan:
			if span, ok := codeSpanRange(source, typed); ok {
				excluded = append(excluded, span)
			}
		case *ast.CodeBlock:
			excluded = append(excluded, lineRanges(source, typed.Lines())...)
		case *ast.FencedCodeBlock:
			excluded = append(excluded, lineRanges(source, typed.Lines())...)
		case *ast.HTMLBlock:
			excluded = append(excluded, lineRanges(source, typed.Lines())...)
			if typed.HasClosure() {
				excluded = append(excluded, physicalLineRange(source, typed.ClosureLine.Start))
			}
		case *ast.RawHTML:
			for index := 0; index < typed.Segments.Len(); index++ {
				segment := typed.Segments.At(index)
				excluded = append(excluded, markdownprofile.Span{Start: segment.Start, End: segment.Stop})
			}
		}
		return ast.WalkContinue, nil
	})

	candidates := scanInlineDestinations(source, mergeMarkdownSpans(excluded))
	matched, err := matchInlineDestinations(inline, candidates)
	if err != nil {
		for _, spec := range inline {
			if _, needed, _ := movedMarkdownDestination(sourcePath, finalPath, spec.destination, from, to); needed {
				return nil, err
			}
		}
		// No inline destination needs a rewrite, so a conservative locator
		// mismatch does not affect this move.
		matched = nil
	}

	var replacements []byteReplacement
	for index, occurrence := range matched {
		newPath, needed, destinationErr := movedMarkdownDestination(sourcePath, finalPath, inline[index].destination, from, to)
		if destinationErr != nil {
			return nil, destinationErr
		}
		if !needed {
			continue
		}
		replacement, replacementErr := replaceDestinationPath(source, occurrence, inline[index].destination, newPath)
		if replacementErr != nil {
			return nil, replacementErr
		}
		replacements = append(replacements, replacement)
	}

	for _, definition := range definitionOrder {
		key := util.ToLinkReference(definition.Label)
		destination, used := usedReferences[key]
		if !used {
			continue
		}
		newPath, needed, destinationErr := movedMarkdownDestination(sourcePath, finalPath, destination, from, to)
		if destinationErr != nil {
			return nil, destinationErr
		}
		if !needed {
			continue
		}
		span, locateErr := locateReferenceDestination(source, definition)
		if locateErr != nil {
			return nil, locateErr
		}
		replacement, replacementErr := replaceDestinationPath(source, span, destination, newPath)
		if replacementErr != nil {
			return nil, replacementErr
		}
		replacements = append(replacements, replacement)
	}
	return replacements, nil
}

func commonMarkDestination(source []byte) string {
	value := util.UnescapePunctuations(source)
	value = util.ResolveNumericReferences(value)
	value = util.ResolveEntityNames(value)
	return string(value)
}

func movedMarkdownDestination(sourcePath, finalPath, destination, from, to string) (string, bool, error) {
	target, directoryOnly, external, valid := resolveLocalDestination(sourcePath, destination)
	if external || !valid {
		return "", false, nil
	}
	intended := target
	if target == from && !directoryOnly {
		intended = to
	}
	current, currentDirectory, currentExternal, currentValid := resolveLocalDestination(finalPath, destination)
	if !currentExternal && currentValid && current == intended && currentDirectory == directoryOnly {
		return "", false, nil
	}
	return relativeLogicalDestination(parentLogical(finalPath), intended, directoryOnly), true, nil
}

func resolveLocalDestination(sourcePath, destination string) (target string, directoryOnly, external, valid bool) {
	if hasDestinationScheme(destination) {
		return "", false, true, true
	}
	pathPart := destination
	if index := strings.IndexAny(pathPart, "?#"); index >= 0 {
		pathPart = pathPart[:index]
	}
	if pathPart == "" {
		return sourcePath, false, false, true
	}
	if strings.HasPrefix(pathPart, "/") || strings.Contains(pathPart, "\\") {
		return "", false, false, false
	}
	directoryOnly = strings.HasSuffix(pathPart, "/")
	segments := strings.Split(pathPart, "/")
	base := parentLogical(sourcePath)
	stack := []string{}
	if base != "." {
		stack = strings.Split(base, "/")
	}
	for index, segment := range segments {
		if segment == "" {
			if index == len(segments)-1 && directoryOnly {
				continue
			}
			return "", false, false, false
		}
		switch segment {
		case ".":
			continue
		case "..":
			if len(stack) == 0 {
				return "", false, false, false
			}
			stack = stack[:len(stack)-1]
		default:
			if segment != "README.md" && !validDestinationComponent(segment) {
				return "", false, false, false
			}
			stack = append(stack, segment)
		}
	}
	if len(stack) == 0 {
		return ".", directoryOnly, false, true
	}
	return strings.Join(stack, "/"), directoryOnly, false, true
}

func validDestinationComponent(value string) bool {
	return snapshot.ValidContentName(value)
}

func hasDestinationScheme(value string) bool {
	if value == "" || !asciiLetter(value[0]) {
		return false
	}
	for index := 1; index < len(value); index++ {
		character := value[index]
		if character == ':' {
			return true
		}
		if !asciiLetter(character) && (character < '0' || character > '9') && character != '+' && character != '.' && character != '-' {
			return false
		}
	}
	return false
}

func asciiLetter(value byte) bool {
	return value >= 'A' && value <= 'Z' || value >= 'a' && value <= 'z'
}

func relativeLogicalDestination(fromDirectory, target string, directoryOnly bool) string {
	var fromParts, targetParts []string
	if fromDirectory != "." {
		fromParts = strings.Split(fromDirectory, "/")
	}
	if target != "." {
		targetParts = strings.Split(target, "/")
	}
	common := 0
	for common < len(fromParts) && common < len(targetParts) && fromParts[common] == targetParts[common] {
		common++
	}
	parts := make([]string, 0, len(fromParts)-common+len(targetParts)-common)
	for range len(fromParts) - common {
		parts = append(parts, "..")
	}
	parts = append(parts, targetParts[common:]...)
	result := strings.Join(parts, "/")
	if result == "" {
		result = "."
	}
	if directoryOnly {
		if result == "." {
			return "./"
		}
		return result + "/"
	}
	return result
}

func replaceDestinationPath(source []byte, occurrence markdownDestinationSpan, semantic, newPath string) (byteReplacement, error) {
	if occurrence.start < 0 || occurrence.end < occurrence.start || occurrence.end > len(source) {
		return byteReplacement{}, errors.New("Markdown destination source span is invalid")
	}
	raw := source[occurrence.start:occurrence.end]
	if commonMarkDestination(raw) != semantic {
		return byteReplacement{}, errors.New("unsupported or ambiguous Markdown destination presentation")
	}
	semanticSuffix := strings.IndexAny(semantic, "?#")
	rawSuffix := len(raw)
	if semanticSuffix < 0 {
		semanticSuffix = len(semantic)
	} else {
		matches := 0
		for index, character := range raw {
			if character != '?' && character != '#' {
				continue
			}
			if commonMarkDestination(raw[:index]) == semantic[:semanticSuffix] && commonMarkDestination(raw[index:]) == semantic[semanticSuffix:] {
				rawSuffix = index
				matches++
			}
		}
		if matches != 1 {
			return byteReplacement{}, errors.New("unsupported encoded Markdown destination suffix boundary")
		}
	}
	encoded := newPath
	if !occurrence.angle {
		encoded = strings.NewReplacer("\\", "\\\\", "(", "\\(", ")", "\\)").Replace(encoded)
	}
	return byteReplacement{start: occurrence.start, end: occurrence.start + rawSuffix, value: []byte(encoded)}, nil
}

func scanInlineDestinations(source []byte, excluded []markdownprofile.Span) []markdownDestinationSpan {
	var result []markdownDestinationSpan
	var brackets []int
	excludedIndex := 0
	for position := 0; position < len(source); {
		for excludedIndex < len(excluded) && position >= excluded[excludedIndex].End {
			excludedIndex++
		}
		if excludedIndex < len(excluded) && position >= excluded[excludedIndex].Start {
			position = excluded[excludedIndex].End
			brackets = nil
			continue
		}
		if source[position] == '\\' && position+1 < len(source) && util.IsPunct(source[position+1]) {
			position += 2
			continue
		}
		switch source[position] {
		case '`':
			run := byteRun(source, position, '`')
			if close := findByteRun(source, position+run, '`', run); close >= 0 {
				position = close + run
				continue
			}
			position += run
			continue
		case '[':
			brackets = append(brackets, position)
		case ']':
			if len(brackets) != 0 {
				opener := brackets[len(brackets)-1]
				brackets = brackets[:len(brackets)-1]
				if position+1 < len(source) && source[position+1] == '(' {
					if candidate, end, ok := parseInlineDestination(source, position+1); ok {
						candidate.image = opener > 0 && source[opener-1] == '!' && !escapedAt(source, opener-1)
						result = append(result, candidate)
						position = end
						continue
					}
				}
			}
		}
		position++
	}
	return result
}

func parseInlineDestination(source []byte, opening int) (markdownDestinationSpan, int, bool) {
	position := opening + 1
	position = skipMarkdownSpace(source, position)
	if position >= len(source) {
		return markdownDestinationSpan{}, opening + 1, false
	}
	if source[position] == ')' {
		return markdownDestinationSpan{start: position, end: position, destination: ""}, position + 1, true
	}
	span, next, ok := parseDestinationToken(source, position)
	if !ok {
		return markdownDestinationSpan{}, opening + 1, false
	}
	position = skipMarkdownSpace(source, next)
	if position < len(source) && source[position] == ')' {
		span.destination = commonMarkDestination(source[span.start:span.end])
		return span, position + 1, true
	}
	if position >= len(source) || source[position] != '"' && source[position] != '\'' && source[position] != '(' || position == next {
		return markdownDestinationSpan{}, opening + 1, false
	}
	opener := source[position]
	closer := opener
	if opener == '(' {
		closer = ')'
	}
	position++
	for position < len(source) {
		if source[position] == '\\' && position+1 < len(source) && util.IsPunct(source[position+1]) {
			position += 2
			continue
		}
		if source[position] == closer {
			position++
			position = skipMarkdownSpace(source, position)
			if position < len(source) && source[position] == ')' {
				span.destination = commonMarkDestination(source[span.start:span.end])
				return span, position + 1, true
			}
			return markdownDestinationSpan{}, opening + 1, false
		}
		position++
	}
	return markdownDestinationSpan{}, opening + 1, false
}

func parseDestinationToken(source []byte, position int) (markdownDestinationSpan, int, bool) {
	if source[position] == '<' {
		start := position + 1
		for index := start; index < len(source) && source[index] != '\n'; {
			if source[index] == '\\' && index+1 < len(source) && util.IsPunct(source[index+1]) {
				index += 2
				continue
			}
			if source[index] == '>' {
				return markdownDestinationSpan{start: start, end: index, angle: true}, index + 1, true
			}
			index++
		}
		return markdownDestinationSpan{}, position, false
	}
	start := position
	opened := 0
	for position < len(source) && source[position] != '\n' {
		character := source[position]
		if character == '\\' && position+1 < len(source) && util.IsPunct(source[position+1]) {
			position += 2
			continue
		}
		if character == '(' {
			opened++
		} else if character == ')' {
			opened--
			if opened < 0 {
				break
			}
		} else if character == ' ' || character == '\t' {
			break
		}
		position++
	}
	return markdownDestinationSpan{start: start, end: position}, position, position > start
}

func matchInlineDestinations(specs []markdownLinkSpec, candidates []markdownDestinationSpan) ([]markdownDestinationSpan, error) {
	if len(specs) == 0 {
		return nil, nil
	}
	var solutions [][]markdownDestinationSpan
	var search func(int, int, []markdownDestinationSpan)
	search = func(specIndex, candidateIndex int, selected []markdownDestinationSpan) {
		if len(solutions) > 1 {
			return
		}
		if specIndex == len(specs) {
			solutions = append(solutions, append([]markdownDestinationSpan(nil), selected...))
			return
		}
		for index := candidateIndex; index < len(candidates); index++ {
			candidate := candidates[index]
			spec := specs[specIndex]
			if candidate.image != spec.image || candidate.destination != spec.destination {
				continue
			}
			search(specIndex+1, index+1, append(selected, candidate))
		}
	}
	search(0, 0, nil)
	if len(solutions) != 1 {
		return nil, errors.New("unsupported or ambiguous Markdown link presentation")
	}
	return solutions[0], nil
}

func locateReferenceDestination(source []byte, definition *ast.LinkReferenceDefinition) (markdownDestinationSpan, error) {
	if definition == nil || definition.Lines().Len() == 0 {
		return markdownDestinationSpan{}, errors.New("reference definition source span is unavailable")
	}
	start := physicalLineRange(source, definition.Lines().At(0).Start).Start
	last := definition.Lines().At(definition.Lines().Len() - 1)
	end := physicalLineRange(source, last.Stop).End
	if end < start || end > len(source) {
		return markdownDestinationSpan{}, errors.New("reference definition source span is invalid")
	}
	var matches []markdownDestinationSpan
	for position := start; position < end; position++ {
		lineStart := position == 0 || source[position-1] == '\n'
		if !lineStart {
			continue
		}
		cursor := position
		for cursor < end && cursor-position < 4 && source[cursor] == ' ' {
			cursor++
		}
		if cursor >= end || source[cursor] != '[' {
			continue
		}
		labelStart := cursor + 1
		labelEnd := findUnescaped(source, labelStart, end, ']')
		if labelEnd < 0 || labelEnd+1 >= end || source[labelEnd+1] != ':' {
			continue
		}
		if util.ToLinkReference(source[labelStart:labelEnd]) != util.ToLinkReference(definition.Label) {
			continue
		}
		cursor = skipMarkdownSpace(source, labelEnd+2)
		if cursor >= end {
			continue
		}
		span, _, ok := parseDestinationToken(source[:end], cursor)
		if ok && commonMarkDestination(source[span.start:span.end]) == commonMarkDestination(definition.Destination) {
			span.destination = commonMarkDestination(definition.Destination)
			matches = append(matches, span)
		}
	}
	if len(matches) != 1 {
		return markdownDestinationSpan{}, errors.New("unsupported or ambiguous Markdown reference destination presentation")
	}
	return matches[0], nil
}

func findUnescaped(source []byte, start, end int, wanted byte) int {
	for position := start; position < end; position++ {
		if source[position] == '\\' && position+1 < end && util.IsPunct(source[position+1]) {
			position++
			continue
		}
		if source[position] == wanted {
			return position
		}
	}
	return -1
}

func skipMarkdownSpace(source []byte, position int) int {
	for position < len(source) && (source[position] == ' ' || source[position] == '\t' || source[position] == '\n') {
		position++
	}
	return position
}

func escapedAt(source []byte, position int) bool {
	backslashes := 0
	for position > 0 && source[position-1] == '\\' {
		position--
		backslashes++
	}
	return backslashes%2 != 0
}

func byteRun(source []byte, position int, value byte) int {
	start := position
	for position < len(source) && source[position] == value {
		position++
	}
	return position - start
}

func findByteRun(source []byte, position int, value byte, length int) int {
	for position < len(source) {
		if source[position] != value {
			position++
			continue
		}
		run := byteRun(source, position, value)
		if run == length {
			return position
		}
		position += run
	}
	return -1
}

func codeSpanRange(source []byte, span *ast.CodeSpan) (markdownprofile.Span, bool) {
	if span == nil || span.FirstChild() == nil || span.LastChild() == nil {
		return markdownprofile.Span{}, false
	}
	first, firstOK := span.FirstChild().(*ast.Text)
	last, lastOK := span.LastChild().(*ast.Text)
	if !firstOK || !lastOK {
		return markdownprofile.Span{}, false
	}
	start := first.Segment.Start
	for start > 0 && source[start-1] == '`' {
		start--
	}
	end := last.Segment.Stop
	for end < len(source) && source[end] == '`' {
		end++
	}
	return markdownprofile.Span{Start: start, End: end}, start < first.Segment.Start && end > last.Segment.Stop
}

func lineRanges(source []byte, lines *text.Segments) []markdownprofile.Span {
	if lines == nil {
		return nil
	}
	result := make([]markdownprofile.Span, 0, lines.Len())
	for index := 0; index < lines.Len(); index++ {
		result = append(result, physicalLineRange(source, lines.At(index).Start))
	}
	return result
}

func physicalLineRange(source []byte, position int) markdownprofile.Span {
	position = min(max(position, 0), len(source))
	start := bytes.LastIndexByte(source[:position], '\n') + 1
	if relative := bytes.IndexByte(source[position:], '\n'); relative >= 0 {
		return markdownprofile.Span{Start: start, End: position + relative + 1}
	}
	return markdownprofile.Span{Start: start, End: len(source)}
}

func mergeMarkdownSpans(spans []markdownprofile.Span) []markdownprofile.Span {
	sort.Slice(spans, func(i, j int) bool {
		if spans[i].Start != spans[j].Start {
			return spans[i].Start < spans[j].Start
		}
		return spans[i].End < spans[j].End
	})
	result := make([]markdownprofile.Span, 0, len(spans))
	for _, span := range spans {
		span.Start = max(0, span.Start)
		if span.End <= span.Start {
			continue
		}
		if len(result) == 0 || span.Start > result[len(result)-1].End {
			result = append(result, span)
			continue
		}
		result[len(result)-1].End = max(result[len(result)-1].End, span.End)
	}
	return result
}
