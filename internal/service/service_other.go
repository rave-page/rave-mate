//go:build !windows && !linux && !darwin

package service

import "errors"

var errUnsupported = errors.New("service install not supported on this platform")

func Install() error          { return errUnsupported }
func Uninstall() error        { return errUnsupported }
func Status() (string, error) { return "", errUnsupported }
