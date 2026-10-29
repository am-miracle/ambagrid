package modbus_test

import (
	"strings"
	"testing"

	"edge-agent-go/internal/adapter/modbus"
)

const victronMap = "../../../../../deploy/modbus/victron-battery.yaml"

func TestLoadMapReadsTheVictronBatteryMap(t *testing.T) {
	m, err := modbus.LoadMap(victronMap)
	if err != nil {
		t.Fatal(err)
	}
	if m.DeviceType != "battery_bms" || m.Manufacturer != "victron" || len(m.Points) != 4 {
		t.Fatalf("map = %+v", m)
	}
	soc := m.Points[3]
	if soc.Field != modbus.FieldBatterySOC || soc.Address != 266 || soc.Function != modbus.Holding ||
		soc.Type != modbus.U16 || soc.Scale != 0.1 || soc.Unit != "%" || !soc.Optional || soc.Unavailable == nil || *soc.Unavailable != 65535 {
		t.Fatalf("SoC point = %+v", soc)
	}
}

func TestParseMapRejectsMistakes(t *testing.T) {
	point := func(extra string) string {
		return "device_type: smart_meter\nmanufacturer: m\nmodel: x\npoints:\n  - field: voltage\n    address: 1\n    function: holding\n    type: u16\n    scale: 1\n    unit: V\n" + extra
	}
	for name, c := range map[string]struct{ yaml, want string }{
		"unknown key":           {point("    scael: 2\n"), "scael"},
		"wrong unit":            {strings.Replace(point(""), "unit: V", "unit: mV", 1), "unit"},
		"32-bit without order":  {strings.Replace(point(""), "type: u16", "type: i32", 1), "word_order"},
		"word order on 16-bit":  {point("    word_order: little\n"), "word_order"},
		"unknown type":          {strings.Replace(point(""), "type: u16", "type: u64", 1), "type"},
		"unknown function":      {strings.Replace(point(""), "function: holding", "function: coil", 1), "function"},
		"zero scale":            {strings.Replace(point(""), "scale: 1", "scale: 0", 1), "scale"},
		"duplicate field":       {point("  - field: voltage\n    address: 2\n    function: input\n    type: u16\n    scale: 1\n    unit: V\n"), "more than once"},
		"optional irradiance":   {"device_type: solar_inverter\nmanufacturer: m\nmodel: x\npoints:\n  - field: solar_irradiance\n    address: 1\n    function: input\n    type: u16\n    scale: 1\n    unit: W/m2\n    optional: true\n", "optional"},
		"address past the end":  {strings.Replace(strings.Replace(point(""), "address: 1", "address: 65535", 1), "type: u16", "type: f32\n    word_order: big", 1), "address"},
		"unknown device type":   {strings.Replace(point(""), "smart_meter", "toaster", 1), "device_type"},
		"no points":             {"device_type: smart_meter\nmanufacturer: m\nmodel: x\n", "points"},
		"unavailable too large": {point("    unavailable: 70000\n"), "unavailable"},
	} {
		if _, err := modbus.ParseMap([]byte(c.yaml)); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: error = %v, want one mentioning %q", name, err, c.want)
		}
	}
}
