package bundle

import (
	"encoding/json"
	"slices"
)

type labelSet map[string]string

type label struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

func (set labelSet) MarshalJSON() ([]byte, error) {
	keys := make([]string, 0, len(set))
	for key := range set {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return json.Marshal(mapItems(keys, func(key string) label {
		return label{Key: key, Value: set[key]}
	}))
}

func (set *labelSet) UnmarshalJSON(raw []byte) error {
	var labels []label
	if err := decodeStrict(raw, &labels); err != nil {
		return err
	}
	if len(labels) > maxFields {
		return ErrLimit
	}
	out := make(labelSet, len(labels))
	for _, label := range labels {
		if _, duplicate := out[label.Key]; duplicate {
			return ErrFormat
		}
		out[label.Key] = label.Value
	}
	*set = out
	return nil
}
