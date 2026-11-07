// Package model holds the normalized, typed device state that every protocol
// decoder fills in and that is published to MQTT. Fields a protocol does not
// provide stay nil and are omitted from the JSON, so consumers can tell
// "not reported" from zero.
package model

// Battery is the normalized state of a battery / BMS.
type Battery struct {
	Device    string `json:"device"`
	Protocol  string `json:"protocol"`
	TS        int64  `json:"ts"`
	Stale     bool   `json:"stale"`
	BusErrors uint64 `json:"bus_errors"` // CAN: kernel error frames. RS485: failed polls.

	VoltageV     *float64 `json:"voltage_v,omitempty"`
	CurrentA     *float64 `json:"current_a,omitempty"` // positive = charging
	PowerW       *float64 `json:"power_w,omitempty"`   // computed: voltage * current
	SOCPct       *float64 `json:"soc_pct,omitempty"`
	SOHPct       *float64 `json:"soh_pct,omitempty"`
	TemperatureC *float64 `json:"temperature_c,omitempty"`

	ChargeVoltageLimitV    *float64 `json:"charge_voltage_limit_v,omitempty"`
	ChargeCurrentLimitA    *float64 `json:"charge_current_limit_a,omitempty"`
	DischargeCurrentLimitA *float64 `json:"discharge_current_limit_a,omitempty"`
	DischargeVoltageLimitV *float64 `json:"discharge_voltage_limit_v,omitempty"`

	RemainingAh *float64 `json:"remaining_ah,omitempty"`
	FullAh      *float64 `json:"full_ah,omitempty"`      // full-charge capacity
	InstalledAh *float64 `json:"installed_ah,omitempty"` // nominal capacity, whole Ah in most dialects
	Cycles      *int     `json:"cycles,omitempty"`
	Modules     *int     `json:"modules,omitempty"`

	CellMinMV    *float64 `json:"cell_min_mv,omitempty"`
	CellMaxMV    *float64 `json:"cell_max_mv,omitempty"`
	CellDeltaMV  *float64 `json:"cell_delta_mv,omitempty"`
	CellTempMinC *float64 `json:"cell_temp_min_c,omitempty"`
	CellTempMaxC *float64 `json:"cell_temp_max_c,omitempty"`
	CellsMV      []int    `json:"cells_mv,omitempty"` // only when every cell has been received

	// Other temperatures (RS485 protocols that report them).
	CellTempsC   []float64 `json:"cell_temps_c,omitempty"`
	TempsC       []float64 `json:"temps_c,omitempty"` // all sensors in the protocol's order, role not identified
	MosfetTempC  *float64  `json:"mosfet_temp_c,omitempty"`
	AmbientTempC *float64  `json:"ambient_temp_c,omitempty"`

	// Raw bytes of fields whose bit meaning has not been verified, as hex.
	ProtectionRaw   string `json:"protection_raw,omitempty"`
	AlarmRaw        string `json:"alarm_raw,omitempty"`
	RequestFlagsRaw string `json:"request_flags_raw,omitempty"`
	StatusRaw       string `json:"status_raw,omitempty"`
	BalanceMaskRaw  string `json:"balance_mask_raw,omitempty"`

	// Decoded flag names, only for protocols whose bit meanings are documented.
	// Present (possibly with empty lists) means "decoded, nothing set".
	Flags *Flags `json:"flags,omitempty"`

	Manufacturer string `json:"manufacturer,omitempty"`
	ID           string `json:"id,omitempty"`
	Serial       string `json:"serial,omitempty"`
	ModelSerial  string `json:"model_serial,omitempty"`
	Version      string `json:"version,omitempty"`
}

// Flags are warning / protection / status bit names that are set.
type Flags struct {
	Warnings    []string `json:"warnings"`
	Protections []string `json:"protections"`
	Status      []string `json:"status"`
}

// F returns a pointer to v.
func F(v float64) *float64 { return &v }

// I returns a pointer to v.
func I(v int) *int { return &v }

// Ready reports whether the core values (voltage, current, SOC) have all been
// received, so a first publish never contains a half-filled record.
func (b *Battery) Ready() bool {
	return b.VoltageV != nil && b.CurrentA != nil && b.SOCPct != nil
}

// Normalized returns a copy ready to publish: a partly received cell list is
// dropped, and derived values (cell spread) are filled in when missing.
func (b Battery) Normalized() Battery {
	for _, c := range b.CellsMV {
		if c == 0 {
			b.CellsMV = nil
			break
		}
	}
	if b.CellDeltaMV == nil && b.CellMinMV != nil && b.CellMaxMV != nil {
		b.CellDeltaMV = F(*b.CellMaxMV - *b.CellMinMV)
	}
	return b
}
