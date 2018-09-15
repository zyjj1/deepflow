package config

import (
	"errors"
	"io/ioutil"
	"net"
	"os"
	"strings"
	"time"

	"github.com/op/go-logging"
	"gopkg.in/yaml.v2"

	"gitlab.x.lan/yunshan/droplet/flowgenerator"
)

var log = logging.MustGetLogger("config")

type Config struct {
	ControllerIps     []string          `yaml:"controller-ips,flow"`
	ControllerPort    uint16            `yaml:"controller-port"`
	LogFile           string            `yaml:"log-file"`
	LogLevel          string            `yaml:"log-level"`
	StatsdServer      string            `yaml:"statsd-server"`
	Profiler          bool              `yaml:"profiler"`
	DataInterfaces    []string          `yaml:"data-interfaces,flow"`
	TapInterfaces     []string          `yaml:"tap-interfaces,flow"`
	Zeroes            []IpPortConfig    `yaml:"zeroes,flow"`
	Stream            IpPortConfig      `yaml:"stream,flow"`
	FlowTimeout       FlowTimeoutConfig `yaml:"flow-timeout"`
	QueueSize         uint32            `yaml:"queue-size"`
	AdapterQueueCount uint32            `yaml:"adapter-queue-count"`
	FlowQueueCount    uint32            `yaml:"flow-queue-count"`
	FlowCountLimit    uint32            `yaml:"flow-count-limit"`
	PolicyMapSize     uint32            `yaml:"policy-map-size"`
}

type IpPortConfig struct {
	Ip   string `yaml:"ip"`
	Port int    `yaml:"port"`
}

// unit: second
type FlowTimeoutConfig struct {
	ForceReportInterval time.Duration `yaml:"force-report-interval"`
	Established         time.Duration `yaml:"established"`
	ClosingRst          time.Duration `yaml:"closing-rst"`
	Others              time.Duration `yaml:"others"`
}

func (c *Config) Validate() error {
	if len(c.ControllerIps) == 0 {
		return errors.New("controller-ips is empty")
	}

	for _, ipString := range c.ControllerIps {
		if net.ParseIP(string(ipString)) == nil {
			return errors.New("controller-ips invalid")
		}
	}

	if c.LogFile == "" {
		c.LogFile = "/var/log/droplet/droplet.log"
	}
	level := strings.ToLower(c.LogLevel)
	levels := map[string]interface{}{"error": nil, "warn": nil, "info": nil, "debug": nil}
	_, ok := levels[level]
	if ok {
		c.LogLevel = level
	} else {
		c.LogLevel = "info"
	}

	if net.ParseIP(c.StatsdServer) == nil {
		return errors.New("Malformed statsd-server")
	}

	if c.FlowTimeout.ForceReportInterval == 0 {
		c.FlowTimeout.ForceReportInterval = flowgenerator.FORCE_REPORT_INTERVAL
	} else {
		c.FlowTimeout.ForceReportInterval *= time.Second
	}
	if c.FlowTimeout.Established == 0 {
		c.FlowTimeout.Established = flowgenerator.TIMEOUT_ESTABLISHED
	} else {
		c.FlowTimeout.Established *= time.Second
	}
	if c.FlowTimeout.ClosingRst == 0 {
		c.FlowTimeout.ClosingRst = flowgenerator.TIMEOUT_ESTABLISHED_RST
	} else {
		c.FlowTimeout.ClosingRst *= time.Second
	}
	if c.FlowTimeout.Others == 0 {
		c.FlowTimeout.Others = flowgenerator.TIMEOUT_EXPCEPTION
	} else {
		c.FlowTimeout.Others *= time.Second
	}
	if c.QueueSize == 0 {
		c.QueueSize = 65536
	}
	if c.AdapterQueueCount == 0 {
		c.AdapterQueueCount = 1
	}
	if c.FlowQueueCount == 0 {
		c.FlowQueueCount = 1
	}
	if c.FlowCountLimit == 0 {
		c.FlowCountLimit = 1024 * 1024
	}

	return nil
}

func Load(path string) Config {
	configBytes, err := ioutil.ReadFile(path)
	if err != nil {
		log.Error("Read config file error:", err)
		os.Exit(1)
	}
	config := Config{}
	if err = yaml.Unmarshal(configBytes, &config); err != nil {
		log.Error("Unmarshal yaml error:", err)
		os.Exit(1)
	}

	if err = config.Validate(); err != nil {
		log.Error(err)
		os.Exit(1)
	}
	return config
}
