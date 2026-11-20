package hap

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

// CharValue is a write or subscribe request for a single characteristic.
// Pointers are used, because "value": false and "ev": false are valid requests.
type CharValue struct {
	AID    uint64 `json:"aid"`
	IID    uint64 `json:"iid"`
	Value  *any   `json:"value,omitempty"`
	Event  *bool  `json:"ev,omitempty"`
	Status *int   `json:"status,omitempty"`
}

type charValues struct {
	Value []CharValue `json:"characteristics"`
}

// WriteCharacters sends PUT /characteristics with any mix of "value" and "ev".
// Returns per characteristic statuses if the accessory answers 207 Multi-Status.
func (c *Client) WriteCharacters(values ...CharValue) ([]CharValue, error) {
	body, err := json.Marshal(charValues{Value: values})
	if err != nil {
		return nil, err
	}

	res, err := c.Put(PathCharacteristics, MimeJSON, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}

	// important to "clear" body
	if body, err = io.ReadAll(res.Body); err != nil {
		return nil, err
	}

	if len(body) == 0 {
		return nil, nil // 204 No Content - all OK
	}

	var v charValues
	if err = json.Unmarshal(body, &v); err != nil {
		return nil, err
	}

	for _, item := range v.Value {
		if item.Status != nil && *item.Status != 0 {
			err = errors.Join(err, fmt.Errorf("hap: write %d.%d status %d", item.AID, item.IID, *item.Status))
		}
	}

	return v.Value, err
}

// SubscribeCharacters enables (or disables) event notifications.
func (c *Client) SubscribeCharacters(ev bool, ids ...[2]uint64) error {
	values := make([]CharValue, len(ids))
	for i, id := range ids {
		values[i] = CharValue{AID: id[0], IID: id[1], Event: &ev}
	}
	_, err := c.WriteCharacters(values...)
	return err
}

// ReadCharacters reads values for the list of aid.iid pairs.
func (c *Client) ReadCharacters(ids ...[2]uint64) ([]CharValue, error) {
	var sb strings.Builder
	for i, id := range ids {
		if i > 0 {
			sb.WriteByte(',')
		}
		_, _ = fmt.Fprintf(&sb, "%d.%d", id[0], id[1])
	}

	res, err := c.Get(PathCharacteristics + "?id=" + sb.String())
	if err != nil {
		return nil, err
	}

	body, err := io.ReadAll(res.Body) // read to the end, important for chunked
	if err != nil {
		return nil, err
	}

	var v charValues
	if err = json.Unmarshal(body, &v); err != nil {
		return nil, err
	}
	return v.Value, nil
}

// DecodeEvent reads the body of the EVENT/1.0 message.
func DecodeEvent(r io.Reader) ([]CharValue, error) {
	var v charValues
	if err := json.NewDecoder(r).Decode(&v); err != nil {
		return nil, err
	}
	return v.Value, nil
}

const uuidBaseSuffix = "-0000-1000-8000-0026BB765291"

// ShortType converts Apple defined UUID to the short form:
// "00000022-0000-1000-8000-0026BB765291" => "22". Other UUIDs stay as is.
func ShortType(s string) string {
	s = strings.ToUpper(s)
	if strings.HasSuffix(s, uuidBaseSuffix) {
		s = s[:len(s)-len(uuidBaseSuffix)]
	}
	if len(s) <= 8 {
		if s = strings.TrimLeft(s, "0"); s == "" {
			s = "0"
		}
	}
	return s
}
