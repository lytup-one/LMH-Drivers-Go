package model

import "math"

// Inverter is the normalized state of a hybrid / off-grid inverter. Like
// Battery, a field a protocol does not report stays nil and is omitted from the
// JSON ("not reported" is not the same as zero).
type Inverter struct {
	Device    string `json:"device"`
	Class     string `json:"class"`
	Protocol  string `json:"protocol"`
	TS        int64  `json:"ts"`
	Stale     bool   `json:"stale"`
	BusErrors uint64 `json:"bus_errors"` // failed polls

	State     string `json:"state,omitempty"` // machine state, e.g. "mains powered operation"
	StateCode *int   `json:"state_code,omitempty"`

	// Battery side. Current is positive while charging.
	BatterySOCPct   *float64 `json:"battery_soc_pct,omitempty"`
	BatteryVoltageV *float64 `json:"battery_voltage_v,omitempty"`
	BatteryCurrentA *float64 `json:"battery_current_a,omitempty"`
	BatteryPowerW   *float64 `json:"battery_power_w,omitempty"` // computed: voltage * current
	BatteryTempC    *float64 `json:"battery_temp_c,omitempty"`

	// Charging.
	ChargeState        string   `json:"charge_state,omitempty"`
	ChargePowerW       *float64 `json:"charge_power_w,omitempty"` // PV plus mains
	PVChargeCurrentA   *float64 `json:"pv_charge_current_a,omitempty"`
	MainsChargeCurrent *float64 `json:"mains_charge_current_a,omitempty"`

	// PV inputs (only inputs that report anything).
	PV       []PVInput `json:"pv,omitempty"`
	PVPowerW *float64  `json:"pv_power_w,omitempty"` // computed: sum of the inputs

	// Mains (grid) input and inverter output.
	GridVoltageV    *float64 `json:"grid_voltage_v,omitempty"`
	GridCurrentA    *float64 `json:"grid_current_a,omitempty"`
	GridFrequencyHz *float64 `json:"grid_frequency_hz,omitempty"`
	OutputVoltageV  *float64 `json:"output_voltage_v,omitempty"`
	OutputCurrentA  *float64 `json:"output_current_a,omitempty"`
	OutputFreqHz    *float64 `json:"output_frequency_hz,omitempty"`
	BusVoltageV     *float64 `json:"bus_voltage_v,omitempty"` // internal DC bus

	// Load.
	LoadCurrentA   *float64 `json:"load_current_a,omitempty"`
	LoadPowerW     *float64 `json:"load_power_w,omitempty"` // active
	LoadApparentVA *float64 `json:"load_apparent_va,omitempty"`
	LoadPF         *float64 `json:"load_pf,omitempty"`
	LoadPct        *float64 `json:"load_pct,omitempty"`

	// Temperatures, named as the protocol names them.
	TempsC       map[string]float64 `json:"temps_c,omitempty"`
	FaultCodes   []int              `json:"fault_codes,omitempty"` // current faults (0 = none, omitted)
	Faults       []string           `json:"faults,omitempty"`      // names for the codes the protocol documents
	FaultBitsRaw string             `json:"fault_bits_raw,omitempty"`

	// Energy statistics kept by the inverter.
	Today  *InverterEnergy `json:"today,omitempty"`
	Totals *InverterEnergy `json:"totals,omitempty"`

	Version     string `json:"version,omitempty"`
	SerialNo    string `json:"serial,omitempty"`
	ModelCode   *int   `json:"model_code,omitempty"`
	RS485Addr   *int   `json:"rs485_address,omitempty"`
	RunningDays *int   `json:"running_days,omitempty"`
}

// PVInput is one PV (MPPT) input.
type PVInput struct {
	VoltageV float64 `json:"voltage_v"`
	CurrentA float64 `json:"current_a"`
	PowerW   float64 `json:"power_w"`
}

// InverterEnergy holds either today's or the accumulated counters. Nil = not reported.
type InverterEnergy struct {
	PVEnergyKWh          *float64 `json:"pv_energy_kwh,omitempty"`
	LoadEnergyKWh        *float64 `json:"load_energy_kwh,omitempty"`
	MainsLoadEnergyKWh   *float64 `json:"mains_load_energy_kwh,omitempty"`
	BatteryChargeAh      *float64 `json:"battery_charge_ah,omitempty"`
	BatteryDischargeAh   *float64 `json:"battery_discharge_ah,omitempty"`
	MainsChargeAh        *float64 `json:"mains_charge_ah,omitempty"`
	MainsChargeEnergyKWh *float64 `json:"mains_charge_energy_kwh,omitempty"`
	InverterMinutes      *float64 `json:"inverter_minutes,omitempty"`
	BypassMinutes        *float64 `json:"bypass_minutes,omitempty"`
	InverterHours        *float64 `json:"inverter_hours,omitempty"`
	BypassHours          *float64 `json:"bypass_hours,omitempty"`
}

// Ready reports whether the core values have been received, so a first publish
// never contains a half-filled record.
func (i *Inverter) Ready() bool { return i.BatteryVoltageV != nil && i.StateCode != nil }

// Finish fills in the derived values before publication.
func (i *Inverter) Finish() {
	i.Class = "inverter"
	if i.BatteryVoltageV != nil && i.BatteryCurrentA != nil {
		i.BatteryPowerW = F(math.Round(*i.BatteryVoltageV**i.BatteryCurrentA*10) / 10)
	}
	i.PVPowerW = nil
	if len(i.PV) > 0 {
		var s float64
		for _, p := range i.PV {
			s += p.PowerW
		}
		i.PVPowerW = F(s)
	}
}
