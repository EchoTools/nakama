package developerportal

import (
	"embed"
	"io/fs"
	"net/http"
	"path"
)

//go:embed ui/dist/*
var embedFS embed.FS
var UIFS = &uiFS{}

type uiFS struct {
	Nt bool
}

func (fs *uiFS) Open(name string) (fs.File, error) {
	return embedFS.Open(path.Join("ui", "dist", name))
}

var UI = http.FileServer(http.FS(UIFS))
