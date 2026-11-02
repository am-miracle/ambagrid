package config

import "fmt"

// AdapterKind is parsed at configuration ingress. Runtime factories select by
// this type rather than interpreting adapter-name strings in several places.
type AdapterKind string

const (
	Simulator AdapterKind = "simulator"
	Modbus    AdapterKind = "modbus"
	MQTT      AdapterKind = "mqtt"
)

func parseAdapterKind(name string) (AdapterKind, error) {
	kind := AdapterKind(name)
	if _, ok := map[AdapterKind]struct{}{Simulator: {}, Modbus: {}, MQTT: {}}[kind]; !ok {
		return "", fmt.Errorf("unknown adapter %q", name)
	}
	return kind, nil
}
func (k *AdapterKind) UnmarshalYAML(unmarshal func(any) error) error {
	var name string
	if err := unmarshal(&name); err != nil {
		return err
	}
	if name == "" {
		*k = ""
		return nil
	}
	kind, err := parseAdapterKind(name)
	if err != nil {
		return err
	}
	*k = kind
	return nil
}
