// C09 的 cron 解析与"下一跳"计算。
//
// 只支持标准 5 段 cron（分 时 日 月 周）。6 段（带秒）会被**结构化拒绝**，而不是
// 把第一段当秒悄悄吞掉：吞掉的后果是"用户以为每分钟跑一次，实际每秒一次"。
//
// 下一跳按**墙钟**搜索，而不是按绝对时间加一分钟。这不是实现细节，是 DST 语义：
//   - 春跳（缺口）：2025-03-09 02:00 America/New_York 不存在。墙钟搜索会走到
//     02:00 这个"不存在但确实被列在日历上"的时刻，再把它解析成缺口结束后的
//     第一个瞬时（03:00 EDT）。按绝对时间搜索则会**跳过** 02:00，任务当天静默消失。
//   - 秋回（重复）：2025-11-02 01:00 出现两次。墙钟只前进一次，所以只会命中
//     较早的那个瞬时（EDT），第二个 01:00（EST）不会被当成新的一跳。
package scheduler

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"my-agent-go/internal/contracts"
)

// maxCronScan 是"下一跳"搜索的迭代上限。墙钟搜索对每个不匹配的小时/日/月都会
// 整段跳过，正常表达式远小于这个数；设上限是为了让"2 月 31 日"这类永不成立的
// 表达式变成一个明确错误，而不是死循环。
const maxCronScan = 500000

// cronSpec 是一条已解析的 cron。位图下标：分钟 0-59、小时 0-23、
// 日 1-31、月 1-12、周 0-6（0 = 周日）。
type cronSpec struct {
	expr    string
	minute  uint64
	hour    uint64
	dom     uint64
	month   uint64
	dow     uint64
	domStar bool
	dowStar bool
}

var monthNames = map[string]int{
	"JAN": 1, "FEB": 2, "MAR": 3, "APR": 4, "MAY": 5, "JUN": 6,
	"JUL": 7, "AUG": 8, "SEP": 9, "OCT": 10, "NOV": 11, "DEC": 12,
}

var dowNames = map[string]int{
	"SUN": 0, "MON": 1, "TUE": 2, "WED": 3, "THU": 4, "FRI": 5, "SAT": 6,
}

// parseCron 解析 5 段 cron。失败一律返回结构化错误码。
func parseCron(expr string) (*cronSpec, error) {
	trimmed := strings.TrimSpace(expr)
	if trimmed == "" {
		return nil, contracts.NewAppError(contracts.CodeConfigMissing, "cron 表达式不能为空", false)
	}
	fields := strings.Fields(trimmed)
	if len(fields) != 5 {
		return nil, contracts.NewAppError(contracts.CodeUnsupported,
			fmt.Sprintf("cron 必须是 5 段（分 时 日 月 周），实际 %d 段: %s"+
				"；不支持带秒的 6 段写法", len(fields), trimmed), false)
	}

	spec := &cronSpec{expr: trimmed}
	var err error
	if spec.minute, err = parseCronField(fields[0], 0, 59, nil, "分钟"); err != nil {
		return nil, err
	}
	if spec.hour, err = parseCronField(fields[1], 0, 23, nil, "小时"); err != nil {
		return nil, err
	}
	if spec.dom, spec.domStar, err = parseCronFieldStar(fields[2], 1, 31, nil, "日"); err != nil {
		return nil, err
	}
	if spec.month, err = parseCronField(fields[3], 1, 12, monthNames, "月"); err != nil {
		return nil, err
	}
	if spec.dow, spec.dowStar, err = parseCronFieldStar(fields[4], 0, 7, dowNames, "周"); err != nil {
		return nil, err
	}
	// 周日两种写法（0 与 7）：7 归一成 0，避免"7 匹配不上"的惊喜。
	if spec.dow&(1<<7) != 0 {
		spec.dow |= 1
		spec.dow &^= 1 << 7
	}
	return spec, nil
}

func parseCronField(raw string, min, max int, names map[string]int, label string) (uint64, error) {
	bits, _, err := parseCronFieldStar(raw, min, max, names, label)
	return bits, err
}

// parseCronFieldStar 解析单个字段，并报告它是不是"整段通配"（* 或 */1）。
//
// dom/dow 的"是否受限"决定两者是 AND 还是 OR（Vixie cron 口径）：
// 两个都受限时按 OR（"每月 1 号或每周一"），只受限一个时按那一个。
func parseCronFieldStar(raw string, min, max int, names map[string]int, label string) (uint64, bool, error) {
	field := strings.TrimSpace(raw)
	if field == "" {
		return 0, false, cronErr(label, raw, "字段为空")
	}
	if field == "*" {
		var bits uint64
		for i := min; i <= max; i++ {
			bits |= 1 << uint(i)
		}
		return bits, true, nil
	}

	var bits uint64
	star := false
	for _, part := range strings.Split(field, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			return 0, false, cronErr(label, raw, "列表里有空项")
		}
		step := 1
		if idx := strings.Index(part, "/"); idx >= 0 {
			stepRaw := strings.TrimSpace(part[idx+1:])
			parsed, err := strconv.Atoi(stepRaw)
			if err != nil || parsed <= 0 {
				return 0, false, cronErr(label, raw, "步长必须是正整数")
			}
			step = parsed
			part = strings.TrimSpace(part[:idx])
			if part == "*" {
				star = star || step == 1
				part = fmt.Sprintf("%d-%d", min, max)
			}
		}
		lo, hi := part, part
		if idx := strings.Index(part, "-"); idx >= 0 {
			lo = strings.TrimSpace(part[:idx])
			hi = strings.TrimSpace(part[idx+1:])
		}
		loVal, ok := cronValue(lo, names)
		if !ok || loVal < min || loVal > max {
			return 0, false, cronErr(label, raw, "取值超出范围")
		}
		hiVal, ok := cronValue(hi, names)
		if !ok || hiVal < min || hiVal > max {
			return 0, false, cronErr(label, raw, "取值超出范围")
		}
		if hiVal < loVal {
			return 0, false, cronErr(label, raw, "区间起点大于终点")
		}
		for i := loVal; i <= hiVal; i += step {
			bits |= 1 << uint(i)
		}
	}
	if bits == 0 {
		return 0, false, cronErr(label, raw, "没有任何取值")
	}
	return bits, star, nil
}

func cronValue(raw string, names map[string]int) (int, bool) {
	if raw == "" {
		return 0, false
	}
	if v, err := strconv.Atoi(raw); err == nil {
		return v, true
	}
	if names != nil {
		if v, ok := names[strings.ToUpper(raw)]; ok {
			return v, true
		}
	}
	return 0, false
}

func cronErr(label, raw, detail string) error {
	return contracts.NewAppError(contracts.CodeUnsupported,
		fmt.Sprintf("cron 的%s字段不合法（%q）：%s", label, raw, detail), false)
}

// dayMatches 按 Vixie cron 的 dom/dow 组合规则判定某一天是否命中。
func (c *cronSpec) dayMatches(wall time.Time) bool {
	domHit := c.dom&(1<<uint(wall.Day())) != 0
	dowHit := c.dow&(1<<uint(int(wall.Weekday()))) != 0
	switch {
	case c.domStar && c.dowStar:
		return true
	case c.domStar:
		return dowHit
	case c.dowStar:
		return domHit
	default:
		return domHit || dowHit
	}
}

// nextAfter 返回严格晚于 after 的下一个计划瞬时。
//
// 搜索在**墙钟**上进行（naive 时间里只看日历字段），命中后再解析成真实瞬时；
// 只有解析结果确实晚于 after 才会被采用——这一条同时挡住秋回里第二次 01:00
// （解析回来是同一个较早瞬时，不大于 after，于是继续往后走）。
func (c *cronSpec) nextAfter(after time.Time, loc *time.Location) (time.Time, error) {
	if loc == nil {
		return time.Time{}, contracts.NewAppError(contracts.CodeConfigMissing, "缺少时区，无法计算下一跳", false)
	}
	local := after.In(loc)
	// 起点：after 所在墙钟分钟的下一分钟。
	wall := time.Date(local.Year(), local.Month(), local.Day(), local.Hour(), local.Minute(), 0, 0, time.UTC).Add(time.Minute)

	for i := 0; i < maxCronScan; i++ {
		if c.month&(1<<uint(int(wall.Month()))) == 0 {
			wall = time.Date(wall.Year(), wall.Month()+1, 1, 0, 0, 0, 0, time.UTC)
			continue
		}
		if !c.dayMatches(wall) {
			wall = time.Date(wall.Year(), wall.Month(), wall.Day()+1, 0, 0, 0, 0, time.UTC)
			continue
		}
		if c.hour&(1<<uint(wall.Hour())) == 0 {
			wall = time.Date(wall.Year(), wall.Month(), wall.Day(), wall.Hour()+1, 0, 0, 0, time.UTC)
			continue
		}
		if c.minute&(1<<uint(wall.Minute())) == 0 {
			wall = wall.Add(time.Minute)
			continue
		}
		if inst := resolveWallClock(loc, wall); inst.After(after) {
			return inst, nil
		}
		wall = wall.Add(time.Minute)
	}
	return time.Time{}, contracts.NewAppError(contracts.CodeUnsupported,
		"cron 表达式在可搜索范围内没有任何下一跳: "+c.expr, false)
}

// resolveWallClock 把一个墙钟时刻解析成真实瞬时。
//
// 两条 DST 规则都在这里，且都只有一种结果：
//   - 重复的墙钟时刻（秋回）：取**较早**的那个瞬时。Go 的 time.Date 在歧义时
//     不保证挑哪一个，所以这里显式枚举候选（±2h/±1h/±30m 覆盖现实中所有偏移差）
//     并取最早的一个。
//   - 不存在的墙钟时刻（春跳）：time.Date 已经把"缺口里的一刻"按切换前的偏移
//     解释成缺口结束后的第一个瞬时（例如 NY 的 02:30 → 03:30 EDT）。
func resolveWallClock(loc *time.Location, wall time.Time) time.Time {
	y, mo, d := wall.Date()
	h, mi := wall.Hour(), wall.Minute()
	base := time.Date(y, mo, d, h, mi, 0, 0, loc)

	var best time.Time
	found := false
	for _, probe := range []time.Duration{
		0, -30 * time.Minute, -time.Hour, -2 * time.Hour,
		30 * time.Minute, time.Hour, 2 * time.Hour,
	} {
		cand := base.Add(probe)
		local := cand.In(loc)
		cy, cmo, cd := local.Date()
		if cy != y || cmo != mo || cd != d || local.Hour() != h || local.Minute() != mi {
			continue
		}
		if !found || cand.Before(best) {
			best = cand
			found = true
		}
	}
	if !found {
		// 缺口（春跳）：这个墙钟时刻根本不存在。口径是“缺口结束后的第一个瞬时”，
		// 而不是“当作它不存在，当天跳过”。time.Date 对缺口的结果不保证落在缺口
		// 之前还是之后（实测会回到 01:00），所以显式向前扫描到墙钟 >= 目标。
		target := time.Date(y, mo, d, h, mi, 0, 0, time.UTC)
		for k := 0; k <= 180; k++ {
			cand := base.Add(time.Duration(k) * time.Minute)
			local := cand.In(loc)
			candWall := time.Date(local.Year(), local.Month(), local.Day(),
				local.Hour(), local.Minute(), 0, 0, time.UTC)
			if !candWall.Before(target) {
				return cand
			}
		}
		return base
	}
	return best
}
