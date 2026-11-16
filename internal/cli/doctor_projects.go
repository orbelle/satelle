package cli

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/bobmcallan/satelle/internal/config"
)

const checkProjectsName = "projects"

func projectPathGone(path string) bool {
	if !filepath.IsAbs(path) {
		return false
	}
	_, err := os.Stat(path)
	return err != nil && errors.Is(err, fs.ErrNotExist)
}

func checkProjects(_ context.Context, _ *doctorInput) ([]doctorIssue, []string) {
	gc, err := config.LoadGlobal()
	if err != nil {
		return nil, []string{checkProjectsName + ": " + err.Error()}
	}
	var out []doctorIssue
	for _, p := range gc.Projects.Paths {
		if projectPathGone(p) {
			out = append(out, doctorIssue{Check: checkProjectsName,
				Problem: p + ": a project of this machine whose path no longer exists",
				Fix:     "satelle project remove " + p, Resolve: resolveRemoveProject, path: p})
		}
	}
	return out, nil
}

func removeLocalProject(path string) (bool, error) {
	gc, err := config.LoadGlobal()
	if err != nil {
		return false, err
	}
	if !gc.Projects.Remove(path) {
		return false, nil
	}
	return true, config.SaveGlobal(gc)
}

func resolveProjects(rep *resolveReport, issues []doctorIssue) {
	for _, is := range issues {
		removed, err := removeLocalProject(is.path)
		switch {
		case err != nil:
			rep.act(is, resultFailed, err.Error())
		case removed:
			rep.act(is, resultRemoved, fmt.Sprintf("%s: removed from this machine's projects: path no longer exists", is.path))
		default:
			rep.act(is, resultKept, is.path+" was not a project on this machine")
		}
	}
}
