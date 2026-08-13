package checker

import (
	"bytes"
	"errors"
	"strconv"
	"strings"

	"github.com/ontopix/engram/internal/documentprofile"
)

var errRoutineDeclaration = errors.New("invalid routine declaration")

// validateRoutineDeclaration checks the closed v1 routine declaration profile.
// It deliberately has no scheduler, clock, or runtime dependency: eligibility
// is configuration that can be validated from the declaration bytes alone.
func validateRoutineDeclaration(data []byte) error {
	document, err := documentprofile.Parse(data)
	if err != nil {
		return errRoutineDeclaration
	}

	root := document.YAML.Root
	if unknownKeys(root, map[string]struct{}{"engram": {}, "cron": {}}) {
		return errRoutineDeclaration
	}
	engram, ok := stringField(root, "engram")
	if !ok || engram != "routine/v1" {
		return errRoutineDeclaration
	}
	cron, ok := stringField(root, "cron")
	if !ok || !validRoutineCron(cron) {
		return errRoutineDeclaration
	}
	if len(bytes.Trim(document.BodyBytes(), " \t\n\r")) == 0 {
		return errRoutineDeclaration
	}
	return nil
}

// validRoutineCron implements annex-routines §4's closed five-field UTC
// profile. It checks syntax and domains only; evaluating eligibility against a
// clock is intentionally outside static validation.
func validRoutineCron(value string) bool {
	fields := strings.Split(value, " ")
	if len(fields) != 5 {
		return false
	}
	for index, domain := range [...]routineCronDomain{
		{minimum: 0, maximum: 59},
		{minimum: 0, maximum: 23},
		{minimum: 1, maximum: 31},
		{minimum: 1, maximum: 12},
		{minimum: 0, maximum: 6},
	} {
		if !validRoutineCronField(fields[index], domain) {
			return false
		}
	}
	// The profile deliberately avoids cron dialects that give day-of-month and
	// day-of-week implementation-specific OR/AND behavior.
	return fields[2] == "*" || fields[4] == "*"
}

type routineCronDomain struct {
	minimum int
	maximum int
}

func (d routineCronDomain) cardinality() int { return d.maximum - d.minimum + 1 }

func validRoutineCronField(field string, domain routineCronDomain) bool {
	if field == "" {
		return false
	}
	for _, item := range strings.Split(field, ",") {
		if !validRoutineCronItem(item, domain) {
			return false
		}
	}
	return true
}

func validRoutineCronItem(item string, domain routineCronDomain) bool {
	if item == "*" {
		return true
	}

	if slash := strings.IndexByte(item, '/'); slash >= 0 {
		if strings.Count(item, "/") != 1 || slash == 0 || slash == len(item)-1 {
			return false
		}
		base, stepText := item[:slash], item[slash+1:]
		cardinality := 0
		switch base {
		case "*":
			cardinality = domain.cardinality()
		default:
			first, last, ok := routineCronRange(base, domain)
			if !ok {
				return false
			}
			cardinality = last - first + 1
		}
		_, ok := routineCronInteger(stepText, 1, cardinality)
		return ok
	}

	if strings.ContainsRune(item, '-') {
		_, _, ok := routineCronRange(item, domain)
		return ok
	}
	_, ok := routineCronInteger(item, domain.minimum, domain.maximum)
	return ok
}

func routineCronRange(value string, domain routineCronDomain) (int, int, bool) {
	if strings.Count(value, "-") != 1 {
		return 0, 0, false
	}
	separator := strings.IndexByte(value, '-')
	if separator == 0 || separator == len(value)-1 {
		return 0, 0, false
	}
	first, firstOK := routineCronInteger(value[:separator], domain.minimum, domain.maximum)
	last, lastOK := routineCronInteger(value[separator+1:], domain.minimum, domain.maximum)
	return first, last, firstOK && lastOK && first <= last
}

// routineCronInteger accepts the profile's arbitrary-length unsigned ASCII
// decimal spelling. Leading zeroes are insignificant, so it normalizes before
// comparing to the small fixed field domains instead of overflowing a machine
// integer while parsing an otherwise valid value such as 0000001.
func routineCronInteger(value string, minimum, maximum int) (int, bool) {
	if value == "" {
		return 0, false
	}
	for index := 0; index < len(value); index++ {
		if value[index] < '0' || value[index] > '9' {
			return 0, false
		}
	}
	normalized := strings.TrimLeft(value, "0")
	if normalized == "" {
		normalized = "0"
	}
	limit := strconv.Itoa(maximum)
	if len(normalized) > len(limit) || len(normalized) == len(limit) && normalized > limit {
		return 0, false
	}
	parsed, err := strconv.Atoi(normalized)
	if err != nil || parsed < minimum {
		return 0, false
	}
	return parsed, true
}
