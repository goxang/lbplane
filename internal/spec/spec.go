package spec

import (
	"bytes"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

const (
	DefaultWeight = 100
	MaxWeight     = 256
)

var (
	namePattern     = regexp.MustCompile(`^[a-z][a-z0-9-]{0,39}$`)
	hostnamePattern = regexp.MustCompile(`^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z]{2,}$`)
)

type Service struct {
	Name        string    `yaml:"name"`
	Owner       string    `yaml:"owner"`
	Hostnames   []string  `yaml:"hostnames"`
	Balance     string    `yaml:"balance"`
	HealthCheck string    `yaml:"health_check"`
	TLSCert     string    `yaml:"tls_cert"`
	Backends    []Backend `yaml:"backends"`

	File string `yaml:"-"`
}

type Backend struct {
	Address string `yaml:"address"`
	Weight  *int   `yaml:"weight"`
}

func (b Backend) EffectiveWeight() int {
	if b.Weight == nil {
		return DefaultWeight
	}
	return *b.Weight
}

// One broken file fails the whole set, so no service is silently dropped.
func LoadDir(dir string) ([]Service, error) {
	paths, err := filepath.Glob(filepath.Join(dir, "*.yaml"))
	if err != nil {
		return nil, err
	}
	sort.Strings(paths)

	var services []Service
	var errs []error
	for _, path := range paths {
		svc, err := loadFile(path)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		services = append(services, svc)
	}
	errs = append(errs, checkConflicts(services)...)
	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	return services, nil
}

func loadFile(path string) (Service, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Service{}, err
	}
	var svc Service
	decoder := yaml.NewDecoder(bytes.NewReader(raw))
	decoder.KnownFields(true)
	if err := decoder.Decode(&svc); err != nil {
		return Service{}, fmt.Errorf("%s: %w", filepath.Base(path), err)
	}
	svc.File = filepath.Base(path)
	svc.normalize()
	if err := svc.Validate(); err != nil {
		return Service{}, fmt.Errorf("%s: %w", svc.File, err)
	}
	return svc, nil
}

func (s *Service) normalize() {
	for i, host := range s.Hostnames {
		s.Hostnames[i] = strings.ToLower(strings.TrimSpace(host))
	}
	if s.Balance == "" {
		s.Balance = "roundrobin"
	}
}

func (s Service) Validate() error {
	var errs []error
	if !namePattern.MatchString(s.Name) {
		errs = append(errs, fmt.Errorf("name %q must match %s", s.Name, namePattern))
	}
	if s.Owner == "" {
		errs = append(errs, errors.New("owner is required"))
	}
	if len(s.Hostnames) == 0 {
		errs = append(errs, errors.New("at least one hostname is required"))
	}
	for _, host := range s.Hostnames {
		if !hostnamePattern.MatchString(host) {
			errs = append(errs, fmt.Errorf("invalid hostname %q", host))
		}
	}
	if s.Balance != "roundrobin" && s.Balance != "leastconn" {
		errs = append(errs, fmt.Errorf("balance %q is not supported, use roundrobin or leastconn", s.Balance))
	}
	if s.HealthCheck != "" && !strings.HasPrefix(s.HealthCheck, "/") {
		errs = append(errs, fmt.Errorf("health_check %q must be a path starting with /", s.HealthCheck))
	}
	if s.TLSCert != "" && (!filepath.IsAbs(s.TLSCert) || strings.ContainsAny(s.TLSCert, " \t")) {
		errs = append(errs, fmt.Errorf("tls_cert %q must be an absolute path without spaces", s.TLSCert))
	}
	if len(s.Backends) == 0 {
		errs = append(errs, errors.New("at least one backend is required"))
	}
	seen := map[string]bool{}
	for _, backend := range s.Backends {
		if err := validateAddress(backend.Address); err != nil {
			errs = append(errs, err)
		}
		if seen[backend.Address] {
			errs = append(errs, fmt.Errorf("backend %s is listed twice", backend.Address))
		}
		seen[backend.Address] = true
		if w := backend.EffectiveWeight(); w < 0 || w > MaxWeight {
			errs = append(errs, fmt.Errorf("backend %s: weight %d out of range 0-%d", backend.Address, w, MaxWeight))
		}
	}
	return errors.Join(errs...)
}

// Runtime API "set server addr" rejects hostnames.
func validateAddress(address string) error {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("backend %q: %w", address, err)
	}
	if net.ParseIP(host) == nil {
		return fmt.Errorf("backend %q: host must be an IP address", address)
	}
	if n, err := strconv.Atoi(port); err != nil || n < 1 || n > 65535 {
		return fmt.Errorf("backend %q: invalid port", address)
	}
	return nil
}

func checkConflicts(services []Service) []error {
	var errs []error
	nameOwner := map[string]string{}
	hostOwner := map[string]string{}
	for _, svc := range services {
		if file, taken := nameOwner[svc.Name]; taken {
			errs = append(errs, fmt.Errorf("%s: service name %q already defined in %s", svc.File, svc.Name, file))
		}
		nameOwner[svc.Name] = svc.File
		for _, host := range svc.Hostnames {
			if file, taken := hostOwner[host]; taken && file != svc.File {
				errs = append(errs, fmt.Errorf("%s: hostname %q already claimed by %s", svc.File, host, file))
			}
			hostOwner[host] = svc.File
		}
	}
	return errs
}
