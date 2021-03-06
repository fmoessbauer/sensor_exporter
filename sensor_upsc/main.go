//
// Copyright 2016 Marios Andreopoulos
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.

// This program is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
// GNU General Public License for more details.

// You should have received a copy of the GNU General Public License
// along with this program.  If not, see <http://www.gnu.org/licenses/>.
//

/*
Package sensor_upsc implements a sensor that uses upsd to get information from
a UPS. It is pretty basic. To add a UPS start sensor_exporter like:

    sensor_exporter uspc,,UPS@HOST

For localhost, HOST may be ommited.

Currently only a few values are reported since I care only about my UPS.
If you are interested to support more values, sumbit a pull request. It is
an easy job, just add entries to upscVarFloat, sensorsType, sensorsHelp. ;)

You can consult the UPSC manual for available readings and their description:
http://networkupstools.org/docs/user-manual.chunked/apcs01.html

Of interest is also the network protocol:
http://networkupstools.org/docs/developer-guide.chunked/ar01s09.html#_command_reference
*/
package sensor_upsc

import (
	"bufio"
	"errors"
	"fmt"
	"log"
	"net"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/fmoessbauer/sensor_exporter/sensor"
)

var suggestedScrapeInterval = time.Duration(10 * time.Second)
var description = `Upsc is a sensor that uses the upsc program to get information from a UPS.
To use it with the suggested scrape interval (HOST may be ommitted for
localhost):

  sensor_exporter upsc,,UPS@HOST`
var timeOut = 10 * time.Second

type Sensor struct {
	Labels     string
	Host       string
	Ups        string
	Re         *regexp.Regexp
	BeginToken string
	EndToken   string
}

// Strings that are used to detect readings from upsd responses. If you add an
// entry to upscVarFloat and your UPS returns this value, the sensor will
// expose it. Please also add a TYPE and HELP entry.
var (
	upscVarFloat = map[string]string{
		"battery.charge"         : "upsc_battery_charge",
		"battery.charge.low"     : "upsc_battery_charge_low",
		"battery.voltage"        : "upsc_battery_voltage",
		"battery.voltage.high"   : "upsc_battery_voltage_high",
		"battery.voltage.low"    : "upsc_battery_voltage_low",
		"battery.voltage.nominal": "upsc_battery_voltage_nominal",
		"input.frequency"        : "upsc_input_frequency",
		"input.frequency.nominal": "upsc_input_frequency_nominal",
		"input.voltage"          : "upsc_input_voltage",
		"input.voltage.fault"    : "upsc_input_voltage_fault",
		"input.voltage.nominal"  : "upsc_input_voltage_nominal",
		"input.current"          : "upsc_input_current",
		"output.voltage"         : "upsc_output_voltage",
		"ups.beeper.status"      : "upsc_ups_beeper_enabled",
		"ups.delay.shutdown"     : "upsc_ups_delay_shutdown",
		"ups.delay.start"        : "upsc_ups_delay_start",
		"ups.load"               : "upsc_ups_load",
		"ups.status"             : "upsc_ups_online",
		"ups.temperature"        : "upsc_ups_temperature",
	}
	sensorsType = []string{
		"# TYPE upsc_battery_charge gauge",
		"# TYPE upsc_battery_charge_low gauge",
		"# TYPE upsc_battery_voltage gauge",
		"# TYPE upsc_battery_voltage_high gauge",
		"# TYPE upsc_battery_voltage_low gauge",
		"# TYPE upsc_battery_voltage_nominal gauge",
		"# TYPE upsc_input_frequency gauge",
		"# TYPE upsc_input_frequency_nominal gauge",
		"# TYPE upsc_input_voltage gauge",
		"# TYPE upsc_input_voltage_fault gauge",
		"# TYPE upsc_input_voltage_nominal gauge",
		"# TYPE upsc_input_current gauge",
		"# TYPE upsc_output_voltage gauge",
		"# TYPE upsc_ups_beeper_enabled gauge",
		"# TYPE upsc_ups_delay_shutdown gauge",
		"# TYPE upsc_ups_delay_start gauge",
		"# TYPE upsc_ups_load gauge",
		"# TYPE upsc_ups_online gauge",
		"# TYPE upsc_ups_temperature gauge",
	}
	sensorsHelp = []string{
		"# HELP upsc_battery_charge gauge Battery charge (percent)",
		"# HELP upsc_battery_charge_low gauge Low battery charge threshold (percent)",
		"# HELP upsc_battery_voltage Battery voltage (V)",
		"# HELP upsc_battery_voltage_high Battery voltage high (V)",
		"# HELP upsc_battery_voltage_low Battery voltage low (V)",
		"# HELP upsc_battery_voltage_nominal Battery voltage nominal / expected (V)",
		"# HELP upsc_input_frequency Input line frequency (Hz)",
		"# HELP upsc_input_frequency_nominal Input line frequency nominal / expected (Hz)",
		"# HELP upsc_input_voltage Input voltage (V)",
		"# HELP upsc_input_voltage_fault Input voltage fault (V)",
		"# HELP upsc_input_voltage_nominal Input voltage nominal / expected (V)",
		"# HELP upsc_input_current Input current (A)",
		"# HELP upsc_output_voltage Output voltage (V)",
		"# HELP upsc_ups_beeper_enabled Beeper is enabled (bool)",
		"# HELP upsc_ups_delay_shutdown Wait number of seconds before shutdown (s)",
		"# HELP upsc_ups_delay_start Start delay after number of seconds (s)",
		"# HELP upsc_ups_load Load on UPS (percent)",
		"# HELP upsc_ups_online UPS is online (bool)",
		"# HELP upsc_ups_temperature UPS temperature (degrees C)",
	}
	sensorStringMapping = map[string]float64{
		"enabled"  : 1,
		"disabled" : 0,
		"OL"       : 2,   // online, charged
		"FSD OL"   : 1.5, // online, forced shutdown 
		"OB"       : 1,   // on battery
		"FSD OB"   : 0.5, // offline, forced shutdown 
		"LB"       : 0,   // low battery
	}
)

func NewSensor(opts string) (sensor.Collector, error) {
	conf := strings.Split(opts, `@`)
	var labels, host, ups string
	switch len(conf) {
	case 2:
		ups = conf[0]
		host = conf[1]
		hostParts := strings.Split(host, `:`) // Do not use port in label
		if len(hostParts) == 1 {              // set default port if needed
			host += ":3493"
		}
		labels = fmt.Sprintf("{ups=\"%s\",host=\"%s\"}", ups, hostParts[0])
	case 1:
		labels = fmt.Sprintf("{ups=\"%s\"}", conf[0])
		ups = conf[0]
		host = "localhost:3493"
	default:
		return nil, errors.New("Upsc, could not understand UPS URI. Empty or too many '@'?. Opts: " + opts)
	}
	// Output is like: VAR UPS ups.load "14"
	reString := "VAR " + ups + " ([a-zA-Z.]*) \"(.*)\""
	re, err := regexp.Compile(reString)
	if err != nil {
		return nil, errors.New("Upsc, could not compile regural expression: " + reString + ". Err: " + err.Error())
	}
	conn, err := net.DialTimeout("tcp", host, timeOut)
	if err != nil {
		log.Printf("Adding upsc sensor at %s but could not connect to remote.\n", host)
	} else {
		defer conn.Close()
	}
	s := Sensor{Labels: labels, Host: host, Ups: ups, Re: re,
		BeginToken: "BEGIN LIST VAR " + ups + "\n", EndToken: "END LIST VAR " + ups + "\n"}
	return s, nil
}

func (s Sensor) Scrape() (out string, e error) {
	conn, err := net.DialTimeout("tcp", s.Host, timeOut)
	if err != nil {
		sensor.Incident()
		log.Printf("Upsc %s@%s, failed to connect: %s\n", s.Ups, s.Host, err.Error())
		return "", nil
	}
	defer conn.Close()
	fmt.Fprintf(conn, "LIST VAR "+s.Ups+"\n")
	reader := bufio.NewReader(conn)

	res, err := reader.ReadString('\n')
	if err != nil {
		sensor.Incident()
		log.Printf("Upsc %s@%s, reading returned error: %s\n", s.Ups, s.Host, err.Error())
		return "", nil
	}
	if res == "ERR UNKNOWN-UPS" {
		sensor.Incident()
		log.Printf("Upsc %s@%s, upsd daemon said \"unknown ups\".\n", s.Ups, s.Host)
		return "", nil
	} else if res != s.BeginToken {
		sensor.Incident()
		log.Printf("Upsc %s@%s, upsd daemon returned unknown response: %s.\n", s.Ups, s.Host, res)
		return "", nil
	}

	var v []string
	for {
		res, err = reader.ReadString('\n')
		//		fmt.Println(res)
		if err != nil {
			sensor.Incident()
			log.Printf("Upsc %s@%s, connection error while reading: %s\n", s.Ups, s.Host, err.Error())
			return "", nil
		}
		v = s.Re.FindStringSubmatch(res)
		if len(v) == 3 {
			if value, exists := upscVarFloat[v[1]]; exists {
				var reading float64
				if mapping, mexists := sensorStringMapping[v[2]]; mexists {
					reading = mapping
				} else {
					reading, err = strconv.ParseFloat(v[2], 64)
					if err != nil {
						sensor.Incident()
						log.Printf("Upsc %s@%s, could not parse %s. Error: %s\n", s.Ups, s.Host, v[1], err.Error())
						break
					}
				}
				out += fmt.Sprintf("%s%s %.2f\n", value, s.Labels, reading)
			}
		}

		if res == s.EndToken {
			break
		}
	}

	return out, nil
}

func init() {
	sensor.RegisterCollector("upsc", NewSensor, suggestedScrapeInterval,
		sensorsType, sensorsHelp, description)
}
