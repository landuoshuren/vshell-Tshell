package model

import "time"

type Flow struct {
	ExportFlow int64 `json:"ExportFlow"`
	InletFlow  int64 `json:"InletFlow"`
	FlowLimit  int64 `json:"FlowLimit"`
}

type Rate struct {
	Value int64 `json:"JGNjuNu"`
}

type Client struct {
	ID            int64     `json:"Id"`
	IsConnect     bool      `json:"IsConnect"`
	VerifyKey     string    `json:"VerifyKey"`
	Tp            string    `json:"Tp"`
	Addr          string    `json:"Addr"`
	Remark        string    `json:"Remark"`
	Status        bool      `json:"Status"`
	LocalIP       string    `json:"LocalIP"`
	UserName      string    `json:"UserName"`
	HostName      string    `json:"HostName"`
	Location      string    `json:"Location"`
	OsName        string    `json:"OsName"`
	ProcessName   string    `json:"ProcessName"`
	PingCheckTime int64     `json:"PingCheckTime"`
	RateLimit     int64     `json:"RateLimit"`
	Flow          Flow      `json:"Flow"`
	Rate          Rate      `json:"Rate"`
	NoStore       bool      `json:"NoStore"`
	NoDisplay     bool      `json:"NoDisplay"`
	MaxConn       int64     `json:"MaxConn"`
	NowConn       int64     `json:"NowConn"`
	ListenerID    int64     `json:"-"`
	LastSeen      time.Time `json:"-"`
}

type Listener struct {
	ID                int64  `json:"Id"`
	Mode              string `json:"Mode"`
	Remark            string `json:"Remark"`
	ListenAddr        string `json:"ListenAddr"`
	ConnectAddr       string `json:"ConnectAddr"`
	WsConnectAddr     string `json:"WsConnectAddr"`
	DNSDomain         string `json:"DNSDomain"`
	PublicDNS         string `json:"PublicDNS"`
	DisconnectTimeout int    `json:"DisconnectTimeout"`
	PingInterval      int    `json:"PingInterval"`
	Vkey              string `json:"Vkey"`
	EncryptSalt       string `json:"EncryptSalt"`
	OssURL            string `json:"OssUrl"`
	OSSKey            string `json:"oss_key,omitempty"`
	Status            bool   `json:"Status"`
	RunStatus         bool   `json:"RunStatus"`
}

type Tunnel struct {
	ID        int64  `json:"Id"`
	ClientID  int64  `json:"ClientId"`
	Remark    string `json:"Remark"`
	Mode      string `json:"Mode"`
	Port      int    `json:"Port"`
	Target    string `json:"Target"`
	Username  string `json:"Username"`
	Password  string `json:"Password"`
	RunStatus bool   `json:"RunStatus"`
	IsConnect bool   `json:"IsConnect"`
	Status    bool   `json:"Status"`
	Flow      Flow   `json:"Flow"`
}

type Settings struct {
	DingdingAccessToken string `json:"dingding_access_token"`
	DingdingKeyWord     string `json:"dingding_key_word"`
	WxKey               string `json:"wx_key"`
}

type State struct {
	NextClientID   int64      `json:"next_client_id"`
	NextListenerID int64      `json:"next_listener_id"`
	NextTunnelID   int64      `json:"next_tunnel_id"`
	Clients        []Client   `json:"clients"`
	Listeners      []Listener `json:"listeners"`
	Tunnels        []Tunnel   `json:"tunnels"`
	Settings       Settings   `json:"settings"`
}

type Hello struct {
	VerifyKey   string `json:"verify_key"`
	Vkey        string `json:"vkey"`
	LocalIP     string `json:"local_ip"`
	UserName    string `json:"user_name"`
	HostName    string `json:"host_name"`
	OSName      string `json:"os_name"`
	ProcessName string `json:"process_name"`
}

type FileItem struct {
	Name  string `json:"name"`
	IsDir bool   `json:"isDir"`
	Time  string `json:"time"`
	Mode  string `json:"mode"`
	Size  int64  `json:"size"`
}
