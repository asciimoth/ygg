package admin

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"

	"github.com/asciimoth/ygg/ygglib/config"
)

type ConfigController struct {
	snapshot map[string]json.RawMessage
	err      error
}

type GetConfigResponse struct {
	Config map[string]json.RawMessage `json:"config"`
}

func NewConfigController(cfg *config.NodeConfig) *ConfigController {
	if cfg == nil {
		return nil
	}
	snapshot, err := snapshotNodeConfig(cfg)
	return &ConfigController{snapshot: snapshot, err: err}
}

func (c *ConfigController) Snapshot() (*GetConfigResponse, error) {
	if c == nil {
		return nil, fmt.Errorf("config controller not configured")
	}
	if c.err != nil {
		return nil, c.err
	}
	return &GetConfigResponse{Config: cloneRawMessageMap(c.snapshot)}, nil
}

func snapshotNodeConfig(cfg *config.NodeConfig) (map[string]json.RawMessage, error) {
	snapshot := map[string]json.RawMessage{}
	value := reflect.ValueOf(cfg).Elem()
	typ := value.Type()
	for i := 0; i < typ.NumField(); i++ {
		field := typ.Field(i)
		if !field.IsExported() {
			continue
		}
		name := jsonFieldName(field)
		if name == "-" {
			continue
		}
		raw, err := json.Marshal(value.Field(i).Interface())
		if err != nil {
			return nil, fmt.Errorf("marshal %s: %w", name, err)
		}
		snapshot[name] = raw
	}
	return snapshot, nil
}

func cloneRawMessageMap(in map[string]json.RawMessage) map[string]json.RawMessage {
	out := make(map[string]json.RawMessage, len(in))
	for key, value := range in {
		out[key] = append(json.RawMessage(nil), value...)
	}
	return out
}

func jsonFieldName(field reflect.StructField) string {
	tag := field.Tag.Get("json")
	name, _, _ := strings.Cut(tag, ",")
	if name == "" {
		return field.Name
	}
	return name
}

func (a *AdminSocket) SetupConfigHandlers(c *ConfigController) {
	if c == nil {
		return
	}
	_ = a.AddHandler(
		"getConfig", "Show the loaded node configuration", []string{},
		func(_ json.RawMessage) (interface{}, error) {
			return c.Snapshot()
		},
	)
}
