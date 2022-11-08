package provider

import (
	"encoding/json"
	"strings"

	"github.com/google/go-containerregistry/pkg/crane"
	"github.com/kairos-io/kairos/pkg/utils"
	"github.com/mudler/go-pluggable"
	"golang.org/x/mod/semver"
)

func eventError(err error) pluggable.EventResponse {
	return pluggable.EventResponse{Error: err.Error()}
}

func ListVersions(e *pluggable.Event) pluggable.EventResponse {
	registry, err := utils.OSRelease("IMAGE_REPO")
	if err != nil {
		return eventError(err)
	}
	tags, err := crane.ListTags(registry)
	if err != nil {
		return eventError(err)
	}

	displayTags := []string{}

	for _, t := range tags {
		if strings.Contains(t, "k3s") {
			displayTags = append(displayTags, t)
		}
	}

	semver.Sort(displayTags)

	if e.Data == "stable" {
		keepOnlyStable(displayTags)
	}

	versions, err := json.Marshal(displayTags)
	resp := &pluggable.EventResponse{
		Data: string(versions),
	}

	if err != nil {
		resp.Error = err.Error()
	}

	return *resp
}

func keepOnlyStable(versions []string) []string {
	result := []string{}
	for _, v := range versions {
		if semver.Prerelease(v) == "" {
			result = append(result, v)
		}
	}

	return result
}
