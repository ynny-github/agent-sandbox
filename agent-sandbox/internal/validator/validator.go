package validator

import "strings"

func Validate(cmd string) error {
	for _, op := range []string{"|", ">", "<", "&", ";", "`", "$("} {
		if strings.Contains(cmd, op) {
			return ErrShellOperator
		}
	}
	return nil
}
