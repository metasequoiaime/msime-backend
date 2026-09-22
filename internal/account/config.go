package account

import (
	"errors"
	"net/mail"
	"net/url"
	"os"
	"strings"
)

type OIDCConfig struct {
	ClientIDs []string `json:"client_ids"`
}
type WechatConfig struct {
	AppID       string `json:"app_id"`
	SecretEnv   string `json:"secret_env"`
	RedirectURI string `json:"redirect_uri"`
}
type MailConfig struct {
	Host        string `json:"host"`
	Port        int    `json:"port"`
	Username    string `json:"username"`
	PasswordEnv string `json:"password_env"`
	From        string `json:"from"`
}
type SMSConfig struct {
	Region             string `json:"region"`
	AccessKeyIDEnv     string `json:"access_key_id_env"`
	AccessKeySecretEnv string `json:"access_key_secret_env"`
	SignName           string `json:"sign_name"`
	TemplateCode       string `json:"template_code"`
}

// 匿名开户。Subject 由客户端生成并连同口令一起保存在本机钥匙串里,服务端只存口令的 HMAC,
// 所以丢了本机凭据就找不回这个账号 —— 这是这种账号的固有代价,不是缺陷。
type AnonymousConfig struct {
	Enabled bool `json:"enabled"`
	// 每个 IP 每天最多开几个,0 表示用默认值。
	DailyPerAddress int `json:"daily_per_address"`
}

type Config struct {
	Enabled     bool   `json:"enabled"`
	DatabaseEnv string `json:"database_env"`
	// 迁移时临时切换到的角色,留空就用连接自己的身份建表。生产上运行角色只有 DML 权限,拿它跑 DDL
	// 必然 permission denied,而缺表是启动失败 —— 于是「版本里新增一张表」等于一次停机(v0.21.0)。
	// 建表的权限属于库的属主角色,让迁移事务 SET ROLE 过去即可,不必给长连接池加 DDL 权限,也不必
	// 为属主另造一套登录凭据 —— 它通常根本不是登录角色。
	MigrationRole string     `json:"migration_role"`
	PepperEnv     string     `json:"pepper_env"`
	Google        OIDCConfig `json:"google"`
	Apple         OIDCConfig `json:"apple"`
	// 匿名账号:装完就有一个可用身份,不必先有邮箱或第三方账号。开着就等于开户没有门槛,所以 begin 那侧
	// 按 IP 限流,否则一段脚本就能刷满数据库和 AI 额度。
	Anonymous AnonymousConfig `json:"anonymous"`
	Wechat    WechatConfig    `json:"wechat"`
	SMS       SMSConfig       `json:"sms"`
	Email     MailConfig      `json:"email"`
}

func (c Config) Validate() error {
	if !c.Enabled {
		return nil
	}
	if os.Getenv(c.DatabaseEnv) == "" || len(os.Getenv(c.PepperEnv)) < 32 {
		return errors.New("用户数据库及至少 32 字节的验证码密钥未配置")
	}
	for _, provider := range []OIDCConfig{c.Apple, c.Google} {
		if len(provider.ClientIDs) > 10 {
			return errors.New("每个提供方最多配置 10 个 Client ID")
		}
		seen := map[string]bool{}
		for _, id := range provider.ClientIDs {
			if id == "" || len(id) > 255 || strings.TrimSpace(id) != id || seen[id] {
				return errors.New("Client ID 必须非空且唯一")
			}
			seen[id] = true
		}
	}
	if c.Wechat.AppID != "" {
		u, e := url.Parse(c.Wechat.RedirectURI)
		if e != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Fragment != "" || os.Getenv(c.Wechat.SecretEnv) == "" {
			return errors.New("微信登录配置无效")
		}
	}
	if c.Email.From != "" {
		a, e := mail.ParseAddress(c.Email.From)
		if e != nil || a.Address != c.Email.From || c.Email.Host == "" || strings.ContainsAny(c.Email.Host, "/:\r\n ") || (c.Email.Port != 465 && c.Email.Port != 587) || c.Email.Username == "" || os.Getenv(c.Email.PasswordEnv) == "" {
			return errors.New("Lark SMTP 配置无效：仅支持 TLS 465 或 STARTTLS 587")
		}
	}
	if c.SMS.TemplateCode != "" && (c.SMS.Region == "" || c.SMS.SignName == "" || os.Getenv(c.SMS.AccessKeyIDEnv) == "" || os.Getenv(c.SMS.AccessKeySecretEnv) == "") {
		return errors.New("阿里云短信配置无效")
	}
	return nil
}
