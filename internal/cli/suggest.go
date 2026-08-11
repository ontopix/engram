package cli

import "strings"

func commandSuggestions(model *Model, group, unknown string) []string {
	const maximumSuggestionInput = 64
	if model == nil || unknown == "" || len(unknown) > maximumSuggestionInput {
		return nil
	}
	seen := make(map[string]bool)
	var candidates []string
	for _, command := range model.Commands {
		var candidate string
		switch {
		case group == "" && len(command.Path) > 0:
			candidate = command.Path[0]
		case group != "" && len(command.Path) == 2 && command.Path[0] == group:
			candidate = command.Path[1]
		}
		if candidate != "" && !seen[candidate] {
			seen[candidate] = true
			candidates = append(candidates, candidate)
		}
	}

	needle := strings.ToLower(unknown)
	best := -1
	var suggestions []string
	for _, candidate := range candidates {
		distance := editDistance(needle, strings.ToLower(candidate))
		if distance > suggestionDistance(len(needle), len(candidate)) {
			continue
		}
		if best < 0 || distance < best {
			best = distance
			suggestions = suggestions[:0]
		}
		if distance == best {
			suggestions = append(suggestions, candidate)
		}
	}
	return suggestions
}

func suggestionDistance(left, right int) int {
	if max(left, right) <= 4 {
		return 1
	}
	return 2
}

func editDistance(left, right string) int {
	distance := make([][]int, len(left)+1)
	for leftIndex := range distance {
		distance[leftIndex] = make([]int, len(right)+1)
		distance[leftIndex][0] = leftIndex
	}
	for rightIndex := range distance[0] {
		distance[0][rightIndex] = rightIndex
	}

	for leftIndex := 1; leftIndex <= len(left); leftIndex++ {
		for rightIndex := 1; rightIndex <= len(right); rightIndex++ {
			replacement := 1
			if left[leftIndex-1] == right[rightIndex-1] {
				replacement = 0
			}
			distance[leftIndex][rightIndex] = min(
				distance[leftIndex-1][rightIndex]+1,
				distance[leftIndex][rightIndex-1]+1,
				distance[leftIndex-1][rightIndex-1]+replacement,
			)
			if leftIndex > 1 && rightIndex > 1 &&
				left[leftIndex-1] == right[rightIndex-2] && left[leftIndex-2] == right[rightIndex-1] {
				distance[leftIndex][rightIndex] = min(
					distance[leftIndex][rightIndex],
					distance[leftIndex-2][rightIndex-2]+1,
				)
			}
		}
	}
	return distance[len(left)][len(right)]
}
