package lbm

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strings"

	"github.com/ArikaChen/lbmanager/pkg/conf"
	"github.com/ArikaChen/lbmanager/pkg/kvstore"
	"github.com/ArikaChen/lbmanager/pkg/utils"
)

const (
	defBufferSize   = 65535
	errFileNotExist = "no such file or directory"

	MaxLen   = 255
	LVSType  = "l4"
	NgType   = "l7"
	TCPProt  = "TCP"
	UDPProt  = "UDP"
	HTTPProt = "HTTP"
	MSICProt = "MISC"

	StatusError  = "ERROR"
	StatusActive = "ACTIVE"
)

var (
	LVSProt       = []string{"TCP", "UDP"}
	LVSHealthProt = []string{"TCP", "HTTP", "MISC"}
	LBType        = []string{"l4", "l7"}
	NginxStrategy = []string{"round_robin", "least_conn"} //"ip_hash", "hash", "least_time"
	NginxProt     = []string{"HTTP", "TCP", "UDP"}
)

type Backend struct {
	IP     string `json:"ip,omitempty"`
	Port   int    `json:"port,omitempty"`
	Weight int    `json:"weight,omitempty"`
}

func (b *Backend) CheckValid() error {
	ip := net.ParseIP(b.IP)
	if ip == nil {
		return fmt.Errorf("backend server IP %s is invalid", b.IP)
	}
	if b.Port > 65535 || b.Port <= 0 {
		return fmt.Errorf("backend server port %d is invalid", b.Port)
	}

	if b.Weight < 0 {
		return fmt.Errorf("backend server weight %d is invalid", b.Weight)
	}
	if b.Weight == 0 {
		b.Weight = 100
	}

	return nil
}

type HealthCheck struct {
	Type       string `json:"type,omitempty"`
	URLPath    string `json:"path,omitempty"`
	StatusCode int    `json:"expectCode,omitempty"`
	Interval   int    `json:"interval,omitempty"`
	Rise       int    `json:"rise,omitempty"` //not used now
	Timeout    int    `json:"timeout,omitempty"`
	MaxRetries int    `json:"retrys,omitempty"`
	Delay      int    `json:"delay,omitempty"`
	Enable     bool   `json:"enable,omitempty"` //not used now, default is true
}

func (h *HealthCheck) CheckValid(t string) error {
	if t == LVSType {
		return h.CheckL4()
	}
	return h.CheckL7()
}

func (h *HealthCheck) CheckL7() error {
	if h.Timeout < 0 {
		return fmt.Errorf("health check timeout %d is invalid", h.Timeout)
	}
	if h.MaxRetries < 0 {
		return fmt.Errorf("health check retry %d is invalid", h.MaxRetries)
	}
	return nil
}

func (h *HealthCheck) CheckL4() error {
	if !utils.IsElementExist(h.Type, LVSHealthProt) {
		return fmt.Errorf("health check type %s is invalid", h.Type)
	}
	if h.Type == HTTPProt {
		if !strings.HasPrefix(h.URLPath, "/") {
			return fmt.Errorf("health check url %s is invalid", h.URLPath)
		}
	}
	if h.Interval <= 0 {
		return fmt.Errorf("interval %d is invalid", h.Interval)
	}
	if h.Timeout <= 0 {
		return fmt.Errorf("health check timeout %d is invalid", h.Timeout)
	}
	if h.MaxRetries <= 0 {
		return fmt.Errorf("health check retry %d is invalid", h.MaxRetries)
	}
	if h.Delay <= 0 {
		return fmt.Errorf("health check delay %d is invalid", h.Delay)
	}
	return nil
}

type ILBCommon interface {
	GetName() string
	GetClusterName() string
}

type LBStatus struct {
	Status   string `json:"status,omitempty"`
	Msg      string `json:"msg,omitempty"`
	IP       string `json:"ip,omitempty"`
	HostName string `json:"hostname,omitempty"`
}

type LBCommon struct {
	ID          string   `json:"id,omitempty"`
	Name        string   `json:"name,omitempty"`
	Type        string   `json:"type,omitempty"`
	ClusterName string   `json:"cluster,omitempty"`
	Protocol    string   `json:"protocol,omitempty"`
	Status      LBStatus `json:"result,omitempty"` //TODO
}

func (l *LBCommon) CheckValid() error {
	if !utils.IsElementExist(l.Type, LBType) {
		return fmt.Errorf("lb type %s is invalid", l.Type)
	}
	err := CheckName(l.Name)
	if err != nil {
		return fmt.Errorf("lb name %s is invalid, %s", l.Name, err)
	}
	err = CheckName(l.ClusterName)
	if err != nil {
		return fmt.Errorf("lb cluster name %s is invalid, %s", l.ClusterName, err)
	}
	return nil
}

func (l *LBCommon) GetName() string {
	return l.Name
}

func (l *LBCommon) GetClusterName() string {
	return l.ClusterName
}

type LVS struct {
	LBCommon `json:",inline"`

	VIP     string      `json:"vip,omitempty"`
	Port    int         `json:"port,omitempty"`
	Conf    LVSConf     `json:"conf,omitempty"`
	Monitor HealthCheck `json:"monitor,omitempty"`
	Servers []Backend   `json:"servers,omitempty"`
}

func NewLVS() *LVS {
	lvs := &LVS{
		LBCommon: LBCommon{
			Type:     LVSType,
			Protocol: TCPProt,
		},
		Conf: LVSConf{
			Strategy: "rr",
			Kind:     "FNAT",
		},
		Monitor: HealthCheck{
			Type:       TCPProt,
			URLPath:    "/",
			StatusCode: 200,
			Interval:   7,
			Timeout:    10,
			MaxRetries: 2,
			Delay:      5,
		},
	}
	return lvs
}

func (l *LVS) Update(item string) error {
	data, err := json.Marshal(l)
	if err != nil {
		return err
	}
	return kvstore.WriteBytes(item, l.Name, data)
}

func (l *LVS) Validate() error {
	err := l.CheckValid()
	if err != nil {
		return err
	}

	if l.Type != LVSType {
		return fmt.Errorf("lb type %s is mismatch", l.Type)
	}

	err = l.CheckVIP()
	if err != nil {
		return err
	}

	err = l.CheckPort()
	if err != nil {
		return err
	}

	err = l.CheckProtocol()
	if err != nil {
		return err
	}

	err = l.Conf.CheckValid()
	if err != nil {
		return fmt.Errorf("lvs conf is invalid, %s", err)
	}

	err = l.Monitor.CheckValid(l.Type)
	if err != nil {
		return err
	}

	var laddr string
	for idx, be := range l.Servers {
		err = be.CheckValid()
		if err != nil {
			return err
		}
		la, err := conf.GetLAddrGroupName(be.IP)
		if err != nil {
			return err
		}
		if idx == 0 {
			laddr = la
		} else {
			if laddr != la {
				return fmt.Errorf("backend server is not in same cidr, %s and %s", l.Servers[0].IP, be.IP)
			}
		}
		// update the default weight
		l.Servers[idx] = be
	}
	return nil
}

func CheckName(name string) error {
	if name == "" {
		return errors.New("name is empty")
	}
	if len(name) > MaxLen {
		return fmt.Errorf("name length is to long")
	}
	return nil
}

func (l *LVS) CheckVIP() error {
	if l.VIP != "" {
		ip := net.ParseIP(l.VIP)
		if ip == nil {
			return fmt.Errorf("lb virtual ip %s is invalid", l.VIP)
		}
	}
	return nil
}

func (l *LVS) CheckPort() error {
	if l.Port > 65535 || l.Port < 0 {
		return fmt.Errorf("lb virtual port %d is invalid", l.Port)
	}
	return nil
}

func (l *LVS) CheckProtocol() error {
	if !utils.IsElementExist(l.Protocol, LVSProt) {
		return fmt.Errorf("lb protocol %s is invalid", l.Protocol)
	}
	if (l.Protocol == TCPProt && l.Monitor.Type == MSICProt) ||
		(l.Protocol == UDPProt && l.Monitor.Type != MSICProt) {
		return fmt.Errorf("lb protocol %s is mismatch with health check type %s", l.Protocol, l.Monitor.Type)
	}
	return nil
}

type SessionPersistence struct {
	Name    string `json:"name,omitempty"`
	Domain  string `json:"domain,omitempty"`
	Path    string `json:"path,omitempty"`
	Expires int    `json:"expire,omitempty"`
	Enable  bool   `json:"enable,omitempty"`
}

func (s *SessionPersistence) CheckValid() error {
	if s.Enable {
		return CheckName(s.Name)
	}
	return nil
}

type Pool struct {
	Name         string             `json:"name,omitempty"`
	Strategy     string             `json:"algo,omitempty"`
	SessionStick SessionPersistence `json:"session,omitempty"`
	Servers      []Backend          `json:"servers,omitempty"`
}

func (p *Pool) CheckValid() error {
	err := CheckName(p.Name)
	if err != nil {
		return fmt.Errorf("lb pool name %s is invalid, %s", p.Name, err)
	}

	if !utils.IsElementExist(p.Strategy, NginxStrategy) {
		return fmt.Errorf("lb pool strategy %s is invalid", p.Strategy)
	}

	err = p.SessionStick.CheckValid()
	if err != nil {
		return nil
	}
	if len(p.Servers) == 0 {
		return fmt.Errorf("lb pool server is empty")
	}
	for _, be := range p.Servers {
		err = be.CheckValid()
		if err != nil {
			return err
		}
	}
	return nil
}

type Location struct {
	URIPath  string `json:"path,omitempty"`
	PoolName string `json:"pool,omitempty"`
}

func (loc *Location) CheckValid() error {
	if loc.URIPath == "" {
		loc.URIPath = "/"
	}
	if !strings.HasPrefix(loc.URIPath, "/") {
		return fmt.Errorf("location url %s is invalid", loc.URIPath)
	}
	return nil
}

type L7Policy struct {
	Name         string   `json:"name,omitempty"`
	Action       string   `json:"action,omitempty"` //"REJECT, REDIRECT_TO_POOL,REDIRECT_TO_URL"
	RedirectPool string   `json:"redirectPool,omitempty"`
	RedirectURL  string   `json:"redirectURL,omitempty"`
	Rules        []L7Rule `json:"rules,omitempty"`
}

type L7Rule struct {
	Type        string `json:"type,omitempty"`    //"HOST_NAME", "PATH", "FILE_TYPE", "HEADER" "COOKIE"
	CompareType string `json:"compare,omitempty"` //"REJECT", "EQUAL" "REGEX"
	Key         string `json:"key,omitempty"`
	Value       string `json:"value,omitempty"`
	Invert      bool   `json:"invert,omitempty"`
}

type Nginx struct {
	LBCommon

	VIP  string `json:"vip,omitempty"`
	Port int    `json:"port,omitempty"`

	Monitor   HealthCheck `json:"monitor,omitempty"`
	Pools     []Pool      `json:"pools,omitempty"`
	Locations []Location  `json:"locations,omitempty"`
	Policy    []L7Policy  `json:"policy,omitempty"`
}

func (n *Nginx) Validate() error {
	err := n.CheckValid()
	if err != nil {
		return err
	}

	if n.Type != NgType {
		return fmt.Errorf("lb type %s is mismatch", n.Type)
	}
	err = n.CheckVIP()
	if err != nil {
		return err
	}

	err = n.CheckPort()
	if err != nil {
		return err
	}

	err = n.CheckProtocol()
	if err != nil {
		return err
	}

	err = n.Monitor.CheckValid(n.Type)
	if err != nil {
		return err
	}

	for _, p := range n.Pools {
		err = p.CheckValid()
		if err != nil {
			return err
		}
	}

	for idx, l := range n.Locations {
		err = l.CheckValid()
		if err != nil {
			return nil
		}
		found := false
		for _, p := range n.Pools {
			if p.Name == l.PoolName {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("location pool name %s is mismatch with pools", l.PoolName)
		}
		n.Locations[idx] = l
	}
	return nil
}

func (n *Nginx) CheckVIP() error {
	if n.VIP != "" {
		ip := net.ParseIP(n.VIP)
		if ip == nil {
			return fmt.Errorf("lb virtual ip %s is invalid", n.VIP)
		}
	}
	return nil
}

func (n *Nginx) CheckPort() error {
	if n.Port > 65535 || n.Port <= 0 {
		return fmt.Errorf("lb virtual port %d is invalid", n.Port)
	}
	return nil
}

func (n *Nginx) CheckProtocol() error {
	if !utils.IsElementExist(n.Protocol, NginxProt) {
		return fmt.Errorf("lb protocol %s is invalid", n.Protocol)
	}
	return nil
}

func NewNginx() *Nginx {
	ng := &Nginx{
		LBCommon: LBCommon{
			Type:     NgType,
			Protocol: HTTPProt,
		},
		Monitor: HealthCheck{
			Type:       HTTPProt,
			URLPath:    "/",
			Timeout:    10,
			MaxRetries: 2,
		},
	}
	return ng
}
