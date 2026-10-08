//go:build !linux && !darwin

package worker

import (
	"errors"
	"os"
)

func openQuestionAnswerMountFileNoFollow(string) (*os.File, error) {
	return nil, errors.New("no-follow clarification input inspection is unsupported on this platform")
}

func questionAnswerMountFileLinkCount(*os.File) (uint64, error) {
	return 0, errors.New("clarification input link inspection is unsupported on this platform")
}
