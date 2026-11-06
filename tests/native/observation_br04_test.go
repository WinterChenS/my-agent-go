//go:build windows

package nativetest

// BR04 的外部观察：页面缩放 1.00/1.50/2.00 + 分栏 20 轮。
//
// 观察源：
//   - zoom-steps.jsonl：每档缩放一行（GetWindowRect/GetClientRect/GetDpiForWindow 的
//     直读值、CDP 读回的 devicePixelRatio、elementFromPoint 命中、WindowFromPoint 的
//     OS 命中、HTTP /__counters 的服务端计数器、截图文件事实）；
//   - splitter-rounds.jsonl：20 轮分栏每轮一行（GetWindowRect 误差、GetParent/GetAncestor）；
//   - screenshots/zoom-*.png：CDP 截图的原始字节。
//
// 事实（zoomSteps / dprValues / clickHits / splitterDriftPx / rounds）全部从这些文件算出。

import (
	"testing"
)

// observeZoomStep 把一档缩放的观察值写进 zoom-steps.jsonl，并落盘该档截图。
func observeZoomStep(t *testing.T, step obsFacts, pagePNG []byte, pngName string) {
	t.Helper()
	if !observationEnabled() {
		return
	}
	observationWrite(t, pngName, pagePNG)
	px := readPNGFacts(t, pngName)
	rec := obsFacts{}
	for k, v := range step {
		rec[k] = v
	}
	rec["screenshot"] = px.obsFacts()
	observationAppendJSONL(t, "zoom-steps.jsonl", rec)
}

// finalizeZoomObservation 从 zoom-steps.jsonl 读回事实并写进 observation.json。
func finalizeZoomObservation(t *testing.T) {
	t.Helper()
	if !observationEnabled() {
		return
	}
	steps := observationReadJSONL(t, "zoom-steps.jsonl")
	dprValues := make([]any, 0, len(steps))
	clickHits := 0
	hitDetails := make([]any, 0, len(steps))
	shaByStep := map[string]any{}
	for _, step := range steps {
		name := step.text(t, "step")
		dprValues = append(dprValues, step.num(t, "devicePixelRatio"))
		shot := step.obj(t, "screenshot")
		shaByStep[name] = shot.text(t, "sha256")
		hit := step.text(t, "hitTest")
		counterBefore := step.num(t, "counterBefore")
		counterAfter := step.num(t, "counterAfter")
		osHit := step.flag(t, "windowFromPointInChildTree")
		ok := hit == "trusted-target" && counterAfter-counterBefore == 1 && osHit
		if ok {
			clickHits++
		}
		hitDetails = append(hitDetails, obsFacts{
			"step":                       name,
			"hitTest":                    hit,
			"counterBefore":              counterBefore,
			"counterAfter":               counterAfter,
			"windowFromPointInChildTree": osHit,
			"hit":                        ok,
		})
	}
	if len(steps) != 3 {
		t.Fatalf("zoom-steps.jsonl 有 %d 行，期望 3 档缩放", len(steps))
	}
	if clickHits != len(steps) {
		t.Fatalf("zoom-steps.jsonl 里只有 %d/%d 档缩放同时满足“命中 trusted-target + 服务端计数器恰好 +1 + OS 落点在子窗口内”",
			clickHits, len(steps))
	}
	observationMergeFacts(t, obsFacts{
		"zoomSteps":                  len(steps),
		"dprValues":                  dprValues,
		"clickHits":                  clickHits,
		"clickHitDetails":            hitDetails,
		"zoomScreenshotSha256ByStep": shaByStep,
		"zoomStepsArtifact":          "zoom-steps.jsonl",
		"zoomObservedBy": "zoom-steps.jsonl 每行由 GetWindowRect/GetClientRect/GetDpiForWindow（OS）、" +
			"Runtime.evaluate devicePixelRatio（页面）、elementFromPoint 命中、WindowFromPoint（OS）、" +
			"HTTP /__counters（fixture 服务端）写成；截图尺寸/颜色数/sha256 从 screenshots/zoom-*.png 读回",
	})
}

// observeSplitterRound 把一轮分栏的观察值写进 splitter-rounds.jsonl。
func observeSplitterRound(t *testing.T, rec obsFacts) {
	t.Helper()
	if !observationEnabled() {
		return
	}
	observationAppendJSONL(t, "splitter-rounds.jsonl", rec)
}

// finalizeSplitterObservation 从 splitter-rounds.jsonl 读回事实并写进 observation.json。
func finalizeSplitterObservation(t *testing.T) {
	t.Helper()
	if !observationEnabled() {
		return
	}
	rounds := observationReadJSONL(t, "splitter-rounds.jsonl")
	maxErr := 0.0
	parentMatched := 0
	zeroWidthRounds := 0
	rectErrByRound := make([]any, 0, len(rounds))
	for _, r := range rounds {
		err := r.num(t, "rectErrPx")
		if err > maxErr {
			maxErr = err
		}
		if r.flag(t, "parentStillMain") && r.flag(t, "rootStillMain") {
			parentMatched++
		}
		if r.flag(t, "zeroWidthRound") {
			zeroWidthRounds++
		}
		rectErrByRound = append(rectErrByRound, obsFacts{
			"round":     r.num(t, "round"),
			"slotWidth": r.num(t, "slotWidthCSS"),
			"rectErrPx": err,
		})
	}
	if len(rounds) != 20 {
		t.Fatalf("splitter-rounds.jsonl 有 %d 行，期望 20 轮", len(rounds))
	}
	if parentMatched != len(rounds) {
		t.Fatalf("splitter-rounds.jsonl 里只有 %d/%d 轮 GetParent/GetAncestor 仍然指向主窗口", parentMatched, len(rounds))
	}
	observationMergeFacts(t, obsFacts{
		"rounds":                         len(rounds),
		"splitterDriftPx":                maxErr,
		"splitterRoundsWithParentIntact": parentMatched,
		"zeroWidthRounds":                zeroWidthRounds,
		"splitterRectErrByRound":         rectErrByRound,
		"splitterArtifact":               "splitter-rounds.jsonl",
		"splitterObservedBy": "splitter-rounds.jsonl 每行由 Present 之后的 GetWindowRect（OS）与期望 rect " +
			"相减得到误差，GetParent/GetAncestor 直读判父子关系，截图尺寸来自 CDP",
	})
}
