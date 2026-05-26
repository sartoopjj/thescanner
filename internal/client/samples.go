package client

import "embed"

//go:embed samples/*.cidr
var samplesFS embed.FS

type SampleList struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	File  string `json:"file"`
	Bytes int    `json:"bytes"`
}

func SampleLists() []SampleList {
	out := []SampleList{}
	entries, err := samplesFS.ReadDir("samples")
	if err != nil {
		return out
	}
	for _, e := range entries {
		info, err := e.Info()
		if err != nil {
			continue
		}
		id := e.Name()
		label := id
		switch id {
		case "regional-lite.cidr":
			label = "Sample list (small)"
		case "regional-full.cidr":
			label = "Sample list (large)"
		}
		out = append(out, SampleList{
			ID:    id,
			Label: label,
			File:  id,
			Bytes: int(info.Size()),
		})
	}
	return out
}

func SampleContent(id string) ([]byte, error) {
	return samplesFS.ReadFile("samples/" + id)
}
