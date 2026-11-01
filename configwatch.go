// configwatch.go —— 配置文件热重载（fsnotify）
//
// 解决的问题：EnvKit 的配置全在 exe 同目录的 config.json 里。以前改一个端口、
// 换一个安装目录、修一下链端 IP，都得关掉重开——而这个程序本身是常驻托盘的服务台。
//
// 三个必须处理的坑，都是踩过之后写下来的：
//
//  1. **不能监听文件本身**。saveExternalConfig 是"写 .tmp → rename 覆盖"，
//     watcher 盯着的那个 inode 在第一次保存后就不存在了，之后永远静默。
//     所以监听的是**父目录**，按 event.Name 过滤 basename。这也是 fsnotify
//     README 里明确写的那条：编辑器/原子保存场景一律监听父目录。
//
//  2. **必须抑制自写回环**。EnvKit 自己保存配置时也会触发事件。判据用
//     内容哈希而不是时间戳：saveExternalConfig 写完把这份 data 的 sha256 登记为
//     "已知"，watcher 读到相同哈希就跳过。时间戳法在这里不成立——DPAPI 每次
//     加密同一明文都会产出不同密文，"我刚写的那份"和"内存里的那份"没法比字节。
//
//  3. **AI 段不跟进**。cfg.AI 由 aiCfgMu 单独保护（aiSnap/aiMutate 都不取 cfgMu），
//     在 cfgMu 下整体替换 cfg 会把 AI 指针一起写掉，与 AI 读者的锁不是同一把。
//     结论：热重载只覆盖 cfgMu 保护的那些段；文件里的 AI 设置与内存不一致时
//     明确提示"重启后生效"——宁可说清楚，也不悄悄改一半。
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/fsnotify/fsnotify"
)

const (
	cfgWatchDebounce = 600 * time.Millisecond
	cfgWatchRetryGap = 1 * time.Second // 读到半截文件时的重试间隔
)

var (
	cfgWatchOnce sync.Once
	cfgReloadMu  sync.Mutex
	// cfgDiskHash 保存"磁盘上那份内容我们已经知道"的 sha256。
	// 用它区分「别人改的」和「我们自己刚写下去的」。
	cfgDiskHash atomic.Value // string
)

func cfgHash(data []byte) string {
	s := sha256.Sum256(data)
	return hex.EncodeToString(s[:])
}

// cfgMarkDisk 登记一份内容为"已知"（自己写的，或刚刚重载过的）。
func cfgMarkDisk(data []byte) { cfgDiskHash.Store(cfgHash(data)) }

func cfgDiskKnown() string {
	if v := cfgDiskHash.Load(); v != nil {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

// cfgWatchStart 启动配置监听（幂等）。调用点必须在 loadConfig 之后 ——
// 启动时要把磁盘当前内容登记为基线，否则第一次事件会被误判成"用户改了"。
func cfgWatchStart() {
	cfgWatchOnce.Do(func() {
		path := configFilePath()
		if b, err := os.ReadFile(path); err == nil {
			cfgMarkDisk(b)
		}
		go cfgWatchLoop(path)
	})
}

func cfgWatchLoop(path string) {
	w, err := fsnotify.NewWatcher()
	if err != nil {
		warn(scSys, "配置", "配置文件变更监听未启动（改配置后需重启才生效）：%v", err)
		return
	}
	defer w.Close()

	dir := filepath.Dir(path)
	if err := w.Add(dir); err != nil {
		warn(scSys, "配置", "配置文件变更监听未启动（%s）：%v", dir, err)
		return
	}
	base := strings.ToLower(filepath.Base(path))
	target := base + ".tmp"

	var pending <-chan time.Time
	var timer *time.Timer
	for {
		select {
		case ev, ok := <-w.Events:
			if !ok {
				return
			}
			name := strings.ToLower(filepath.Base(ev.Name))
			if name != base && name != target {
				continue
			}
			// Chmod 一律忽略：杀软/索引器会无意义地刷这个事件，
			// 跟着它重载等于给用户制造莫须有的"配置变了"。
			if ev.Op&(fsnotify.Write|fsnotify.Create|fsnotify.Rename) == 0 {
				continue
			}
			if timer != nil {
				timer.Stop()
			}
			timer = time.NewTimer(cfgWatchDebounce)
			pending = timer.C
		case err, ok := <-w.Errors:
			if !ok {
				return
			}
			warn(scSys, "配置", "配置文件监听出错（后续变更可能漏掉）：%v", err)
		case <-pending:
			pending = nil
			cfgReloadIfChanged(path)
		}
	}
}

// cfgReloadIfChanged 读盘、比对哈希、必要时整体换掉 cfg（AI 段除外）。
func cfgReloadIfChanged(path string) {
	cfgReloadMu.Lock()
	defer cfgReloadMu.Unlock()

	data, err := os.ReadFile(path)
	if err != nil {
		return // rename 的瞬间可能刚好读不到，等下一次事件
	}
	if cfgHash(data) == cfgDiskKnown() {
		return // 我们自己写的，或者内容确实没变
	}

	var next Config
	if err := json.Unmarshal(data, &next); err != nil {
		// 编辑器可能分块写：半截 JSON 解析失败很正常，隔一秒再看一眼。
		time.Sleep(cfgWatchRetryGap)
		data, err = os.ReadFile(path)
		if err != nil {
			return
		}
		if err := json.Unmarshal(data, &next); err != nil {
			warn(scSys, "配置", "config.json 已变动但解析失败，未重载（继续使用内存中的配置）：%v", err)
			return
		}
		if cfgHash(data) == cfgDiskKnown() {
			return
		}
	}

	normalizeConfig(&next)
	decryptConfig(&next)

	aiChanged, future := cfgApplyReload(next)

	cfgMarkDisk(data)
	refreshLogQuota(devSnap()) // 日志配额走原子快照，锁外推一次（锁序：不能在 cfgMu 里调）
	// 手机访问的开关也要跟着走：文件里把 enabled 改掉而监听不动，
	// 下面那句"无需重启"就是假话。端口变更仍然要重启（lanServeApply 自己判断），
	// 这条调用是幂等的 —— 状态没变时什么都不做。
	lanServeApply(lanSnap())

	info(scSys, "配置", "config.json 已变更，配置已热重载（无需重启）")
	if future {
		warn(scSys, "配置", "该配置来自更新版本的 EnvKit（schema %d > %d），已按本版本已知字段加载",
			next.SchemaVersion, configSchemaVersion)
	}
	if aiChanged {
		warn(scSys, "配置", "文件里的 AI 设置与当前运行值不一致：AI 配置不参与热重载，重启 EnvKit 后生效")
	}
	auditNow(actSys, "config_reload", "config.json", "", resOK, "文件变更触发热重载")
}

// cfgApplyReload 把"已经解析好、已经规范化过"的新配置装进全局。
//
// 单独抽出来有两个原因：
//   - 换 cfg 这几行必须在 cfgMu 内一次性完成（不能先写一半再写一半，读的人会看到混合态）；
//   - "AI 段不许被换掉"是这条路径上最容易写错、也最难被发现的一条，需要能被单测直接钉住。
//
// 返回值：aiChanged = 文件里的 AI 段与内存不同（调用方负责提示"重启生效"）；
// future = 该配置来自更新的 schema 版本。
func cfgApplyReload(next Config) (aiChanged, future bool) {
	cfgMu.Lock()
	defer cfgMu.Unlock()
	cur := cloneConfigDeep(cfg)
	aiChanged = !reflect.DeepEqual(cur.AI, next.AI)
	next.AI = cur.AI // 同一个指针：AI 段由 aiCfgMu 保护，这里不碰
	if next.Dev == nil {
		next.Dev = cur.Dev
	}
	// 已配对的手机**不跟着文件走**。
	//
	// 设备表是 EnvKit 自己维护的（配对 / 移除是唯一写入口），而典型的热重载场景
	// 恰恰是"用户拿编辑器改了别的字段" —— 那个编辑器缓冲区往往是配对之前打开的，
	// 照抄回来等于把刚配好的手机静默踢掉，且没有任何提示。
	if next.Lan != nil && cur.Lan != nil {
		next.Lan.Devices = append([]LanDevice(nil), cur.Lan.Devices...)
	}
	future = next.SchemaVersion > configSchemaVersion
	cfg = next
	return aiChanged, future
}
