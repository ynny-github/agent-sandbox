package validator

import "errors"

var ErrShellOperator = errors.New("shell operators are not allowed: split the command into separate calls and try again")
