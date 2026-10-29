// modbus reads telemetry from Modbus devices described by a YAML register map,
// so a new device model needs a map file rather than a new driver.
package modbus

import (
	"bytes"
	"fmt"
	"os"
	"strings"

	"go.yaml.in/yaml/v2"

	"edge-agent-go/internal/domain"
)

type Field string

const (
	FieldVoltage             Field = "voltage"
	FieldCurrent             Field = "current"
	FieldActivePower         Field = "active_power"
	FieldFrequency           Field = "frequency"
	FieldTotalKWh            Field = "total_kwh"
	FieldInternalTemperature Field = "internal_temperature"
	FieldBatterySOC          Field = "battery_soc"
	FieldSolarIrradiance     Field = "solar_irradiance"
)

// fieldUnits is the unit each reading field is stored in; a point's scale must
// convert the raw register value into exactly this unit.
var fieldUnits = map[Field]string{
	FieldVoltage:             "V",
	FieldCurrent:             "A",
	FieldActivePower:         "kW",
	FieldFrequency:           "Hz",
	FieldTotalKWh:            "kWh",
	FieldInternalTemperature: "C",
	FieldBatterySOC:          "%",
	FieldSolarIrradiance:     "W/m2",
}

type Function string

const (
	Holding Function = "holding"
	Input   Function = "input"
)

type DataType string

const (
	U16 DataType = "u16"
	I16 DataType = "i16"
	U32 DataType = "u32"
	I32 DataType = "i32"
	F32 DataType = "f32"
)

// Words is how many 16-bit registers the type occupies.
func (t DataType) Words() int {
	switch t {
	case U32, I32, F32:
		return 2
	}
	return 1
}

// WordOrder says which register of a 32-bit value holds the high word.
type WordOrder string

const (
	BigWordOrder    WordOrder = "big"
	LittleWordOrder WordOrder = "little"
)

type Map struct {
	DeviceType   domain.AssetType `yaml:"device_type"`
	Manufacturer string           `yaml:"manufacturer"`
	Model        string           `yaml:"model"`
	Points       []Point          `yaml:"points"`
}

type Point struct {
	Field     Field     `yaml:"field"`
	Address   uint16    `yaml:"address"`
	Function  Function  `yaml:"function"`
	Type      DataType  `yaml:"type"`
	WordOrder WordOrder `yaml:"word_order"`
	// Scale multiplies the raw value into the field's unit.
	Scale float64 `yaml:"scale"`
	Unit  string  `yaml:"unit"`
	// Unavailable is the raw value the device reports when it has no reading.
	Unavailable *uint32 `yaml:"unavailable"`
	Optional    bool    `yaml:"optional"`
}

func LoadMap(path string) (Map, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Map{}, fmt.Errorf("read register map: %w", err)
	}
	return ParseMap(data)
}

// ParseMap rejects unknown keys, so a misspelled setting fails at startup
// instead of silently taking its default.
func ParseMap(data []byte) (Map, error) {
	var m Map
	if err := yaml.UnmarshalStrict(bytes.TrimSpace(data), &m); err != nil {
		return Map{}, fmt.Errorf("parse register map: %w", err)
	}
	if err := m.validate(); err != nil {
		return Map{}, fmt.Errorf("register map: %w", err)
	}
	return m, nil
}

func (m Map) validate() error {
	if !m.DeviceType.Valid() {
		return fmt.Errorf("device_type %q is not a known asset type", m.DeviceType)
	}
	if strings.TrimSpace(m.Manufacturer) == "" || strings.TrimSpace(m.Model) == "" {
		return fmt.Errorf("manufacturer and model are required")
	}
	if len(m.Points) == 0 {
		return fmt.Errorf("points must list at least one register")
	}
	seen := make(map[Field]bool, len(m.Points))
	for _, p := range m.Points {
		if err := p.validate(); err != nil {
			return fmt.Errorf("point %q at %d: %w", p.Field, p.Address, err)
		}
		if seen[p.Field] {
			return fmt.Errorf("field %q is mapped more than once", p.Field)
		}
		seen[p.Field] = true
	}
	return nil
}

func (p Point) validate() error {
	unit, known := fieldUnits[p.Field]
	switch {
	case !known:
		return fmt.Errorf("unknown field")
	case p.Unit != unit:
		return fmt.Errorf("unit %q must be %q for this field", p.Unit, unit)
	case p.Function != Holding && p.Function != Input:
		return fmt.Errorf("function %q must be holding or input", p.Function)
	case p.Scale == 0:
		return fmt.Errorf("scale must not be zero")
	// Irradiance has no missing representation in a reading, so making it
	// optional would turn an absent register into a zero.
	case p.Optional && p.Field == FieldSolarIrradiance:
		return fmt.Errorf("solar_irradiance cannot be optional")
	}
	switch p.Type {
	case U16, I16:
		if p.WordOrder != "" {
			return fmt.Errorf("word_order applies only to 32-bit types")
		}
	case U32, I32, F32:
		if p.WordOrder != BigWordOrder && p.WordOrder != LittleWordOrder {
			return fmt.Errorf("32-bit type %s needs word_order big or little", p.Type)
		}
	default:
		return fmt.Errorf("type %q must be u16, i16, u32, i32 or f32", p.Type)
	}
	if int(p.Address)+p.Type.Words() > 1<<16 {
		return fmt.Errorf("address %d leaves no room for %d registers", p.Address, p.Type.Words())
	}
	if p.Unavailable != nil && p.Type.Words() == 1 && *p.Unavailable > 0xffff {
		return fmt.Errorf("unavailable %d does not fit in one register", *p.Unavailable)
	}
	return nil
}
