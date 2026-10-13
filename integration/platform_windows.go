//go:build windows

package integration

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

const platformName = "windows"

const internetSettingsPath = `Software\Microsoft\Windows\CurrentVersion\Internet Settings`

type registryValue struct {
	Exists bool            `json:"存在"`
	Type   uint32          `json:"类型"`
	Data   json.RawMessage `json:"原始值,omitempty"`
}
type windowsProxySnapshot struct {
	Flags       *uint32                  `json:"连接代理标志,omitempty"`
	Values      map[string]registryValue `json:"注册表值"`
	Environment map[string]registryValue `json:"用户环境变量,omitempty"`
}

func captureSystemProxy() (json.RawMessage, error) {
	k, err := registry.OpenKey(registry.CURRENT_USER, internetSettingsPath, registry.QUERY_VALUE)
	if err != nil {
		return nil, fmt.Errorf("读取当前用户系统代理失败：%w", err)
	}
	defer k.Close()
	snapshot := windowsProxySnapshot{Values: make(map[string]registryValue)}
	for _, name := range []string{"ProxyEnable", "ProxyServer", "ProxyOverride", "AutoConfigURL"} {
		value, err := readRegistryValue(k, name)
		if err != nil {
			return nil, fmt.Errorf("读取注册表值 %s 失败：%w", name, err)
		}
		snapshot.Values[name] = value
	}
	environment, err := captureProxyEnvironment()
	if err != nil {
		return nil, err
	}
	snapshot.Environment = environment
	flags, err := queryConnectionFlags()
	if err != nil {
		return nil, err
	}
	snapshot.Flags = &flags
	b, err := json.Marshal(snapshot)
	return b, err
}

func readRegistryValue(k registry.Key, name string) (registryValue, error) {
	size, typ, err := k.GetValue(name, nil)
	if err == registry.ErrNotExist {
		return registryValue{}, nil
	}
	if err != nil {
		return registryValue{}, err
	}
	data := make([]byte, size)
	_, typ, err = k.GetValue(name, data)
	if err != nil {
		return registryValue{}, err
	}
	result := registryValue{Exists: true, Type: typ}
	var value any
	switch typ {
	case registry.DWORD, registry.QWORD:
		v, _, err := k.GetIntegerValue(name)
		if err != nil {
			return registryValue{}, err
		}
		value = v
	case registry.SZ, registry.EXPAND_SZ, registry.LINK:
		v, _, err := k.GetStringValue(name)
		if err != nil {
			return registryValue{}, err
		}
		if typ == registry.LINK {
			return registryValue{}, fmt.Errorf("无法无损恢复链接类型注册表值")
		}
		value = v
	case registry.MULTI_SZ:
		v, _, err := k.GetStringsValue(name)
		if err != nil {
			return registryValue{}, err
		}
		value = v
	default:
		return registryValue{}, fmt.Errorf("不支持备份注册表值类型：%d", typ)
	}
	result.Data, err = json.Marshal(value)
	if err != nil {
		return registryValue{}, err
	}
	return result, nil
}

func applySystemProxy(proxy string) error {
	u, err := url.Parse(proxy)
	if err != nil || u.Hostname() == "" || u.Port() == "" || u.User != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Path != "" || u.RawQuery != "" {
		return fmt.Errorf("代理地址必须包含主机和端口")
	}
	k, _, err := registry.CreateKey(registry.CURRENT_USER, internetSettingsPath, registry.SET_VALUE)
	if err != nil {
		return windowsRegistryError("打开当前用户系统代理设置", err)
	}
	defer k.Close()
	if err := k.SetStringValue("ProxyServer", u.Host); err != nil {
		return fmt.Errorf("设置代理服务器失败：%w", err)
	}
	if err := k.SetStringValue("ProxyOverride", "<local>;localhost;127.0.0.1;[::1]"); err != nil {
		return fmt.Errorf("设置代理旁路失败：%w", err)
	}
	if err := k.SetDWordValue("ProxyEnable", 1); err != nil {
		return fmt.Errorf("启用代理失败：%w", err)
	}
	if err := k.DeleteValue("AutoConfigURL"); err != nil && err != registry.ErrNotExist {
		return err
	}
	if err := setProxyEnvironment(u.String()); err != nil {
		return err
	}
	if err := setConnectionProxy(3, u.Host, "<local>;localhost;127.0.0.1;[::1]", ""); err != nil {
		return err
	}
	if err := broadcastInternetSettings(); err != nil {
		return err
	}
	return nil
}

func restoreSystemProxy(data json.RawMessage) error {
	var snapshot windowsProxySnapshot
	if err := json.Unmarshal(data, &snapshot); err != nil {
		return fmt.Errorf("Windows 代理备份内容无效：%w", err)
	}
	for _, name := range []string{"ProxyEnable", "ProxyServer", "ProxyOverride"} {
		v, ok := snapshot.Values[name]
		if !ok {
			return fmt.Errorf("代理备份缺少注册表值 %s", name)
		}
		if err := validateRegistryValue(v); err != nil {
			return fmt.Errorf("代理备份中的注册表值 %s 无效：%w", name, err)
		}
	}
	if value, ok := snapshot.Values["AutoConfigURL"]; ok {
		if err := validateRegistryValue(value); err != nil {
			return err
		}
	}
	for _, value := range snapshot.Environment {
		if err := validateRegistryValue(value); err != nil {
			return err
		}
	}
	k, _, err := registry.CreateKey(registry.CURRENT_USER, internetSettingsPath, registry.SET_VALUE)
	if err != nil {
		return windowsRegistryError("打开当前用户系统代理设置", err)
	}
	defer k.Close()
	if snapshot.Flags != nil {
		text := func(name string) string {
			var value string
			_ = json.Unmarshal(snapshot.Values[name].Data, &value)
			return value
		}
		if err := setConnectionProxy(*snapshot.Flags, text("ProxyServer"), text("ProxyOverride"), text("AutoConfigURL")); err != nil {
			return err
		}
	}
	for _, name := range []string{"ProxyEnable", "ProxyServer", "ProxyOverride"} {
		v, ok := snapshot.Values[name]
		if !ok {
			return fmt.Errorf("代理备份缺少注册表值 %s", name)
		}
		if err := writeRegistryValue(k, name, v); err != nil {
			return fmt.Errorf("恢复注册表值 %s 失败：%w", name, err)
		}
	}
	if value, ok := snapshot.Values["AutoConfigURL"]; ok {
		if err := writeRegistryValue(k, "AutoConfigURL", value); err != nil {
			return err
		}
	}
	if err := restoreProxyEnvironment(snapshot.Environment); err != nil {
		return err
	}
	return broadcastInternetSettings()
}

func writeRegistryValue(k registry.Key, name string, v registryValue) error {
	if !v.Exists {
		if err := k.DeleteValue(name); err != nil && err != registry.ErrNotExist {
			return err
		}
		return nil
	}
	switch uint32(v.Type) {
	case registry.DWORD, registry.QWORD:
		if uint32(v.Type) == registry.DWORD {
			var n uint32
			if err := json.Unmarshal(v.Data, &n); err != nil {
				return fmt.Errorf("注册表 DWORD 格式无效：%w", err)
			}
			return k.SetDWordValue(name, n)
		}
		var n uint64
		if err := json.Unmarshal(v.Data, &n); err != nil {
			return fmt.Errorf("注册表 QWORD 格式无效：%w", err)
		}
		return k.SetQWordValue(name, n)
	case registry.SZ, registry.EXPAND_SZ:
		var s string
		if err := json.Unmarshal(v.Data, &s); err != nil {
			return fmt.Errorf("注册表文本格式无效：%w", err)
		}
		if uint32(v.Type) == registry.EXPAND_SZ {
			return k.SetExpandStringValue(name, s)
		}
		return k.SetStringValue(name, s)
	case registry.MULTI_SZ:
		var ss []string
		if err := json.Unmarshal(v.Data, &ss); err != nil {
			return fmt.Errorf("注册表列表格式无效：%w", err)
		}
		return k.SetStringsValue(name, ss)
	default:
		return fmt.Errorf("不支持恢复注册表值类型：%d", v.Type)
	}
}

func validateRegistryValue(v registryValue) error {
	if !v.Exists {
		return nil
	}
	switch v.Type {
	case registry.DWORD:
		var n uint32
		return json.Unmarshal(v.Data, &n)
	case registry.QWORD:
		var n uint64
		return json.Unmarshal(v.Data, &n)
	case registry.SZ, registry.EXPAND_SZ:
		var s string
		return json.Unmarshal(v.Data, &s)
	case registry.MULTI_SZ:
		var s []string
		return json.Unmarshal(v.Data, &s)
	default:
		return fmt.Errorf("不支持的注册表值类型：%d", v.Type)
	}
}

func windowsRegistryError(action string, err error) error {
	if errors.Is(err, windows.ERROR_ACCESS_DENIED) {
		return fmt.Errorf("%s权限不足：%w", action, err)
	}
	return fmt.Errorf("%s失败：%w", action, err)
}

func broadcastInternetSettings() error {
	if err := internetSetOption(39); err != nil {
		return fmt.Errorf("广播代理设置变更失败：%w", err)
	}
	if err := internetSetOption(37); err != nil {
		return fmt.Errorf("刷新代理设置失败：%w", err)
	}
	return nil
}

func internetSetOption(option uintptr) error {
	return callInternetSetOption(option)
}

func resolvePlatformApplication(target string) (string, error) {
	if p := existingPath(target); p != "" {
		return p, nil
	}
	return "", fmt.Errorf("应用可执行文件不存在：%s", target)
}

func discoverApplication() string {
	local := os.Getenv("LOCALAPPDATA")
	candidates := []string{}
	if local != "" {
		candidates = append(candidates,
			filepath.Join(local, "Programs", "OpenAI", "Codex", "Codex.exe"),
			filepath.Join(local, "Programs", "ChatGPT", "ChatGPT.exe"),
			filepath.Join(local, "ChatGPT", "ChatGPT.exe"),
			filepath.Join(local, "Programs", "Codex", "Codex.exe"),
			filepath.Join(local, "Codex", "Codex.exe"),
		)
	}
	for _, p := range candidates {
		if found := existingPath(p); found != "" {
			return found
		}
	}
	return ""
}
