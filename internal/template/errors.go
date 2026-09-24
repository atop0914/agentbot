package template

import "errors"

var (
	ErrNotFound         = errors.New("template not found")
	ErrTemplateExists   = errors.New("template already exists")
	ErrInvalidInput     = errors.New("invalid input")
	ErrNotRecording     = errors.New("not recording")
	ErrAlreadyRecording = errors.New("already recording")
	ErrAlreadyPublished = errors.New("template already published to marketplace")
	ErrNotPublished     = errors.New("template not published to marketplace")
	ErrSelfInstall      = errors.New("cannot install your own template")
)
