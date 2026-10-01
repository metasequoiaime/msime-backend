package account

import (
	"encoding/base64"
	"errors"
	"net/mail"
	"net/url"
	"os"
	"strings"
)

type OIDCConfig struct {
	ClientIDs []string `json:"client_ids"`
}

// GoogleDesktopConfig is the "Desktop app" OAuth client used by the loopback server-exchange flow. The secret stays on the server; desktop clients never see it.
type GoogleDesktopConfig struct {
	ClientID  string `json:"client_id"`
	SecretEnv string `json:"secret_env"`
}
type GoogleConfig struct {
	ClientIDs []string            `json:"client_ids"`
	Desktop   GoogleDesktopConfig `json:"desktop"`
}
type WechatConfig struct {
	AppID       string `json:"app_id"`
	SecretEnv   string `json:"secret_env"`
	RedirectURI string `json:"redirect_uri"`
}

// AvatarConfig is the Cloudflare R2 bucket uploaded avatars are stored in. The bucket is public behind PublicBaseURL; the server only writes and deletes. The account ID and the access key pair are read from the named environment variables. An empty Bucket turns uploads off, and users keep their Google picture or the nickname initial.
type AvatarConfig struct {
	Bucket             string `json:"bucket"`
	PublicBaseURL      string `json:"public_base_url"`
	AccountIDEnv       string `json:"account_id_env"`
	AccessKeyIDEnv     string `json:"access_key_id_env"`
	SecretAccessKeyEnv string `json:"secret_access_key_env"`
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
	MigrationRole string       `json:"migration_role"`
	PepperEnv     string       `json:"pepper_env"`
	Google        GoogleConfig `json:"google"`
	Apple         OIDCConfig   `json:"apple"`
	// Names the environment variable holding the std-base64 AES-256 key that encrypts provider refresh tokens at rest. Required once the Google desktop client is configured.
	TokenKeyEnv string `json:"token_key_env"`
	// 匿名账号:装完就有一个可用身份,不必先有邮箱或第三方账号。开着就等于开户没有门槛,所以 begin 那侧
	// 按 IP 限流,否则一段脚本就能刷满数据库和 AI 额度。
	Anonymous AnonymousConfig `json:"anonymous"`
	Wechat    WechatConfig    `json:"wechat"`
	SMS       SMSConfig       `json:"sms"`
	Email     MailConfig      `json:"email"`
	Avatars   AvatarConfig    `json:"avatars"`
}

func (c Config) Validate() error {
	if !c.Enabled {
		return nil
	}
	if os.Getenv(c.DatabaseEnv) == "" || len(os.Getenv(c.PepperEnv)) < 32 {
		return errors.New("用户数据库及至少 32 字节的验证码密钥未配置")
	}
	for _, ids := range [][]string{c.Apple.ClientIDs, c.Google.ClientIDs} {
		if len(ids) > 10 {
			return errors.New("每个提供方最多配置 10 个 Client ID")
		}
		seen := map[string]bool{}
		for _, id := range ids {
			if id == "" || len(id) > 255 || strings.TrimSpace(id) != id || seen[id] {
				//lint:ignore ST1005 the message starts with a proper noun
				return errors.New("Client ID 必须非空且唯一")
			}
			seen[id] = true
		}
	}
	if d := c.Google.Desktop; d.ClientID != "" {
		listed := false
		for _, id := range c.Google.ClientIDs {
			listed = listed || id == d.ClientID
		}
		// The exchanged ID token is checked by the ordinary Google verifier, so the desktop client must be one of its audiences.
		if !listed || os.Getenv(d.SecretEnv) == "" {
			//lint:ignore ST1005 the message starts with a proper noun
			return errors.New("Google 桌面客户端配置无效：client_id 须同时列在 client_ids 中，且密钥环境变量不能为空")
		}
		if _, e := c.providerTokenKey(); e != nil {
			return e
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
			//lint:ignore ST1005 the message starts with a proper noun
			return errors.New("Lark SMTP 配置无效：仅支持 TLS 465 或 STARTTLS 587")
		}
	}
	if a := c.Avatars; a.Bucket != "" {
		u, e := url.Parse(a.PublicBaseURL)
		// Only the configuration itself is checked here. Secrets that are missing from the environment turn uploads off at startup instead (see avatarStorageFor), so an optional feature whose secret has not been provisioned yet cannot keep the whole service down.
		if e != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || strings.Trim(u.Path, "/") != "" || u.RawQuery != "" || u.Fragment != "" || a.AccountIDEnv == "" || a.AccessKeyIDEnv == "" || a.SecretAccessKeyEnv == "" {
			return errors.New("头像存储配置无效：public_base_url 须为 https 域名根地址，账号 ID 与访问密钥环境变量名不能为空")
		}
	}
	if c.SMS.TemplateCode != "" && (c.SMS.Region == "" || c.SMS.SignName == "" || os.Getenv(c.SMS.AccessKeyIDEnv) == "" || os.Getenv(c.SMS.AccessKeySecretEnv) == "") {
		return errors.New("阿里云短信配置无效")
	}
	return nil
}

// providerTokenKey decodes the AES-256 key that seals provider refresh tokens.
func (c Config) providerTokenKey() ([]byte, error) {
	key, e := base64.StdEncoding.DecodeString(os.Getenv(c.TokenKeyEnv))
	if c.TokenKeyEnv == "" || e != nil || len(key) != 32 {
		return nil, errors.New("第三方令牌加密密钥无效：须为标准 base64 编码的 32 字节")
	}
	return key, nil
}
