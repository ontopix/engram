package schemaprofile

import (
	"fmt"
)

func validateDateFormat(value any) error {
	text, ok := value.(string)
	if !ok {
		return nil
	}
	if !validDate(text) {
		return fmt.Errorf("must use exact YYYY-MM-DD Gregorian form")
	}
	return nil
}

func validateDateTimeFormat(value any) error {
	text, ok := value.(string)
	if !ok {
		return nil
	}
	if !validDateTime(text) {
		return fmt.Errorf("must use the exact engram date-time form")
	}
	return nil
}

func validDate(value string) bool {
	if len(value) != 10 || value[4] != '-' || value[7] != '-' {
		return false
	}
	year, ok := decimal(value[0:4])
	if !ok || year == 0 {
		return false
	}
	month, ok := decimal(value[5:7])
	if !ok || month < 1 || month > 12 {
		return false
	}
	day, ok := decimal(value[8:10])
	if !ok || day < 1 || day > daysInMonth(year, month) {
		return false
	}
	return true
}

func validDateTime(value string) bool {
	if len(value) < 20 || !validDate(value[:10]) || value[10] != 'T' || value[13] != ':' || value[16] != ':' {
		return false
	}
	hour, ok := decimal(value[11:13])
	if !ok || hour > 23 {
		return false
	}
	minute, ok := decimal(value[14:16])
	if !ok || minute > 59 {
		return false
	}
	second, ok := decimal(value[17:19])
	if !ok || second > 59 {
		return false
	}

	position := 19
	if position < len(value) && value[position] == '.' {
		position++
		start := position
		for position < len(value) && asciiDigit(value[position]) {
			position++
		}
		if position == start {
			return false
		}
	}
	if position == len(value)-1 && value[position] == 'Z' {
		return true
	}
	if len(value)-position != 6 || value[position] != '+' && value[position] != '-' || value[position+3] != ':' {
		return false
	}
	offsetHour, ok := decimal(value[position+1 : position+3])
	if !ok || offsetHour > 23 {
		return false
	}
	offsetMinute, ok := decimal(value[position+4 : position+6])
	return ok && offsetMinute <= 59
}

func decimal(value string) (int, bool) {
	result := 0
	for index := 0; index < len(value); index++ {
		if !asciiDigit(value[index]) {
			return 0, false
		}
		result = result*10 + int(value[index]-'0')
	}
	return result, true
}

func asciiDigit(value byte) bool {
	return value >= '0' && value <= '9'
}

func daysInMonth(year, month int) int {
	switch month {
	case 4, 6, 9, 11:
		return 30
	case 2:
		if year%4 == 0 && (year%100 != 0 || year%400 == 0) {
			return 29
		}
		return 28
	default:
		return 31
	}
}
