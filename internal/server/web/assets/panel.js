"use strict";
const $ = (id) => document.getElementById(id);
const who = { claude: "Claude", codex: "Codex", grok: "Grok", ai: "AI", dnd: "勿扰", spare: "备用", panel: "面板", cli: "命令行", listener: "口令", schedule: "定时" };
const botWhat = { reminder: "提醒", weather: "天气", alert: "告警", wxbot: "微信" };
const dayName = ["日", "一", "二", "三", "四", "五", "六"];

async function api(path, body, method) {
  const opt = body === undefined ? (method ? { method } : {})
    : { method: method || "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(body) };
  // redirect "manual": behind a login gate an expired session turns API calls into a
  // redirect to the gate's login page, which fetch cannot follow cross-origin. Reload the
  // page instead so the gate can show its login page (otherwise the panel just looks offline).
  const r = await fetch(path, { credentials: "same-origin", redirect: "manual", ...opt });
  if (r.type === "opaqueredirect") { relogin(); throw new Error("login"); }
  if (r.status === 401 && !path.startsWith("/api/auth")) { gate(); throw new Error("login"); }
  const data = r.status === 204 ? null : await r.json().catch(() => null);
  if (!r.ok) throw new Error(zh(data?.error?.message || "HTTP " + r.status));
  return data;
}
// At most one reload a minute, so a gate that keeps redirecting cannot loop the page.
function relogin() {
  const last = Number(sessionStorage.getItem("xs-relogin") || 0);
  if (Date.now() - last < 60000) { banner("登录过期了：关掉面板再打开，重新登录一次。", true); return; }
  sessionStorage.setItem("xs-relogin", String(Date.now()));
  location.reload();
}
function el(tag, cls, text) { const e = document.createElement(tag); if (cls) e.className = cls; if (text != null) e.textContent = text; return e; }
function pad(n) { return String(n).padStart(2, "0"); }
function hm(t) { const d = new Date(t); return pad(d.getHours()) + ":" + pad(d.getMinutes()); }
function dayKey(t) { return new Date(t).toDateString(); }
function dayLabel(t) {
  const d = new Date(t), today = new Date();
  const n = Math.round((new Date(today.toDateString()) - new Date(d.toDateString())) / 864e5);
  return n === 0 ? "今天" : n === 1 ? "昨天" : (d.getMonth() + 1) + " 月 " + d.getDate() + " 日 · 周" + dayName[d.getDay()];
}
function src(s) {
  if (s.startsWith("bot:")) return "bot · " + (botWhat[s.slice(4)] || s.slice(4));
  return who[s] || s;
}

// Error texts from the service, in plain Chinese; the original
// stays on a second line of the tooltip.
const plain = [
  [/PC did not finish|pcmouth: .*time/i, "电脑那边没按时念完，改由音箱念"],
  [/PC not connected/i, "电脑没连上"],
  [/same line said moments ago|duplicate/i, "刚说过一模一样的话，这句没重复念"],
  [/too many lines|rate.?limit/i, "一分钟里话太多了，这句没念"],
  [/too long|80/i, "太长了，最多 80 个字"],
  [/empty/i, "没有能念的字"],
  [/edge-tts|render/i, "晓晓的声音没生成出来，用了小爱原声"],
  [/unauthori[sz]ed|401/, "小米登录失效了"],
  [/70022|risk/i, "小米暂时限流"],
  [/context canceled|deadline/i, "等太久没结果"],
  [/husic/i, "Husic 连不上"],
  [/fetch clip|HTTP 4|HTTP 5/i, "取音频失败"],
];
function zh(msg) {
  if (!msg) return "";
  if (/[\u4e00-\u9fff]/.test(msg) && !/[A-Za-z]{6,}/.test(msg)) return msg;
  for (const [re, t] of plain) if (re.test(msg)) return t;
  return "出了点问题";
}
function zhTitle(msg) { const t = zh(msg); return t === msg ? msg : t + "\n" + msg; }

function banner(msg, bad) {
  const b = $("banner");
  if (!msg) { b.hidden = true; return; }
  b.hidden = false; b.textContent = msg; b.className = "banner" + (bad ? " bad" : "");
}
function light(id, state) { $(id).className = "light" + (state ? " " + state : ""); }

// ---- 正在 ----
let lastNow = "";
function setNow(title, sub, spinning) {
  const key = title + "|" + sub;
  const box = $("now-title").parentElement;
  $("disc").className = "disc " + (spinning ? "spin" : "idle");
  if (key === lastNow) return;
  const first = !lastNow; lastNow = key;
  const apply = () => { $("now-title").textContent = title; $("now-sub").textContent = sub; box.classList.remove("swap"); };
  if (first) return apply();
  box.classList.add("swap"); setTimeout(apply, 220);
}
function setVolume(v) { $("volume").value = v; $("volume-value").textContent = v; paintVolume(); }
function paintVolume() { const r = $("volume"); r.style.setProperty("--p", ((r.value - r.min) / (r.max - r.min)) * 100 + "%"); }

let pcOn = false, husicWired = false, dndOn = false;
let music = null; // { where, paused } while something plays
let musicHold = 0; // until then a click's local state beats a status read
let lastStatus = null;
function drawMusic() {
  const s = lastStatus; if (!s) return;
  const paused = music ? music.paused : false;
  const place = s.where === "pc" ? "电脑 · Husic" : "音箱上";
  if (s.playing) setNow(s.playing, [s.artist, place, paused ? "暂停了" : ""].filter(Boolean).join(" · "), !paused);
  else if (s.state === "speaking") setNow("在说话", "", true);
  else if (!s.husic_wired) setNow(s.online ? "音箱在线" : "音箱没回应", s.pc ? "电脑开着 · 从电脑出声" : "电脑没开 · 音箱念", false);
  else setNow("安静着", s.pc ? "电脑开着 · 放歌交给 Husic" : "", false);
  $("play").classList.toggle("playing", !!music && !paused);
  $("play").setAttribute("aria-label", music && !paused ? "暂停" : music ? "继续" : "放");
  // The speaker cannot resume a pushed song where it stopped.
  $("play").title = music && paused && s.where !== "pc" ? "继续（音箱上会从这首的开头放）" : "";
  $("next").disabled = !music;
}
async function status() {
  let s;
  try { s = await api("/api/status"); }
  catch (e) { if (e.message !== "login") { light("l-speaker", "warn"); banner("面板连不上服务了，过一会儿会自己重试。", false); } return; }
  light("l-speaker", s.online ? "ok" : "warn");
  light("l-mi", s.login === "ok" ? "ok" : s.login === "renewing" ? "warn" : "bad");
  light("l-pc", s.pc ? "ok" : "");
  pcOn = !!s.pc;
  $("route").textContent = s.dictating ? "主人在说话 · 说完再念" : s.voice === "native" ? "小爱原声 · 音箱念" : s.pc ? "电脑开着 · 从电脑出声" : "电脑没开 · 音箱念";
  light("l-husic", s.husic ? "ok" : ""); $("l-husic").hidden = !s.husic_wired;
  dndOn = !!s.dnd;
  $("dnd-btn").setAttribute("aria-pressed", String(dndOn));
  $("dnd-text").textContent = dndOn ? "勿扰到 " + s.dnd_until : "勿扰";
  $("dnd-btn").title = dndOn ? "点一下关掉勿扰" : "打开勿扰：说的话只记下不出声";
  // Without Husic there is no music: only stop and the volume stay.
  document.querySelector(".transport").hidden = !s.husic_wired;
  // Without music the card is just the speaker: its state and the volume.
  document.querySelector(".now").classList.toggle("speech-only", !s.husic_wired);
  $("ears").hidden = !s.husic_wired; // voice commands only start music
  document.querySelector('#e-action [data-a="play"]').hidden = !s.husic_wired;
  husicWired = !!s.husic_wired;
  if (s.login === "none") banner("还没登录小米：点右上角「设置」登录，再选一台音箱。", true);
  else if (s.error && /选音箱|选一台/.test(s.error)) banner("账号下有好几台音箱：点右上角「设置」选一台。", true);
  else if (s.login === "login-required") banner("小米要重新登录了：点右上角「设置」重新登录，手机会收到一条验证码。", true);
  else if (s.login === "risk-control") banner("小米暂时限流，两小时后会自己恢复。", true);
  else if (!s.online) banner("音箱没回应：看看音箱有没有通电、联网。", false);
  else banner("");
  if (document.activeElement !== $("volume") && s.volume != null && Date.now() - volTouched > 3000) setVolume(s.volume);
  // A click is shown at once and wins over a status read that may predate
  // it (Husic reports back a moment later).
  if (Date.now() < musicHold && music && s.playing) s.paused = music.paused;
  music = s.playing ? { where: s.where, paused: !!s.paused } : null;
  lastStatus = s;
  drawMusic();
  $("volume-of").textContent = s.pc ? "电脑音量" : "音箱音量";
  $("volume").closest(".volume").title = s.pc ? "电脑开着：调的是整台电脑的音量（蓝牙连着音箱，音箱跟着变）" : "音箱自己的音量";
  for (const b of $("voice").children) b.setAttribute("aria-checked", String(b.dataset.v === s.voice));
}

// ---- 对话：说过的话在左，对小爱说的在右 ----
const seen = new Set();
let firstHistory = true;
async function history() {
  let h; try { h = await api("/api/history"); } catch { return; }
  $("listen").checked = h.listener;
  drawEar(h.listener ? h.ear : null);
  heardRuns = h.heard.filter((x) => x.action);
  const items = [
    ...h.says.map((s) => ({ at: s.at, key: "s" + s.id, say: s })),
    ...h.heard.slice(0, 30).map((x) => ({ at: x.at, key: "h" + x.at, heard: x })),
  ].sort((a, b) => new Date(a.at) - new Date(b.at));
  const list = $("msgs");
  const stick = list.scrollHeight - list.scrollTop - list.clientHeight < 40;
  list.textContent = "";
  if (!items.length) list.append(el("li", "empty", "说过的话、你对小爱说的话都会出现在这里。"));
  let day = "";
  for (const it of items) {
    if (dayKey(it.at) !== day) { day = dayKey(it.at); list.append(el("li", "day", dayLabel(it.at))); }
    const li = el("li", "msg");
    if (it.say) {
      const s = it.say;
      const how = s.err ? "没念成" : s.voice === "quiet" ? "勿扰中 · 没念" : s.voice === "pc" ? "电脑" : s.voice === "edge" ? "音箱 · 晓晓" : s.voice === "native" ? "音箱 · 原声" : lastStatus && lastStatus.dictating ? "等主人说完" : "排队中";
      const meta = el("div", "meta");
      meta.append(el("span", null, src(s.source)), el("span", null, hm(s.at)), el("span", s.err ? "err" : null, how));
      if (s.err) meta.title = zhTitle(s.err);
      if (!s.voice && !s.err) li.classList.add("wait");
      li.append(meta, el("div", "bubble", s.text));
    } else {
      const x = it.heard;
      li.classList.add("mine");
      const meta = el("div", "meta");
      meta.append(el("span", null, "对小爱说"), el("span", null, hm(x.at)));
      li.append(meta, el("div", "bubble", x.query));
      if (x.action) li.append(el("div", "did" + (x.action.startsWith("没") ? " bad" : ""), "→ " + x.action));
    }
    if (!firstHistory && !seen.has(it.key)) li.classList.add("fresh");
    seen.add(it.key);
    list.append(li);
  }
  if (firstHistory || stick) list.scrollTop = list.scrollHeight;
  firstHistory = false;
  drawToday();
}
// The light by 听口令: reading what was said to Xiao Ai keeps failing
// (night it failed 140 times without anyone seeing).
function drawEar(e) {
  const dot = $("ear-dot"), base = $("ears").dataset.tip || ($("ears").dataset.tip = $("ears").title);
  if (!e || !e.reads) { dot.className = "ear-dot"; $("ears").title = base; return; }
  const r = e.failures / e.reads;
  dot.className = "ear-dot" + (r > 0.5 ? " bad" : r > 0.2 ? " warn" : "");
  $("ears").title = r > 0.2 ? "最近 5 分钟读小爱对话记录 " + e.reads + " 次，失败 " + e.failures + " 次（" + zh(e.last_err) + "），口令会慢或听不到\n\n" + base : base;
}
$("listen").addEventListener("change", (e) => {
  api("/api/settings", { listener: e.target.checked }).then(history).catch((x) => { e.target.checked = !e.target.checked; banner(x.message, true); });
});
$("say").addEventListener("submit", async (ev) => {
  ev.preventDefault();
  const text = $("text").value.trim(); if (!text) return;
  $("send").disabled = true;
  try { await api("/api/say", { text }); $("text").value = ""; setTimeout(history, 300); }
  catch (e) { banner("没送出去：" + e.message, true); }
  finally { $("send").disabled = false; }
});
$("voice").addEventListener("click", (e) => {
  const v = e.target.dataset?.v; if (!v) return;
  for (const b of $("voice").children) b.setAttribute("aria-checked", String(b === e.target));
  api("/api/settings", { voice: v }).then(status).catch((x) => banner(x.message, true));
});

// ---- 走带键与音量 ----
let volTimer, volTouched = 0;
$("volume").addEventListener("input", (e) => {
  volTouched = Date.now();
  $("volume-value").textContent = e.target.value; paintVolume();
  clearTimeout(volTimer);
  volTimer = setTimeout(() => api("/api/volume", { volume: +e.target.value }).catch((x) => banner("音量没改成：" + x.message, true)), 120);
});
// Music buttons go to whoever plays: the PC's Husic while the PC is on
// (the server never touches the speaker then), our speaker session otherwise.
const musicDo = (action) => {
  musicHold = Date.now() + 2500;
  return api("/api/music/" + action, undefined, "POST")
    .then(() => { for (const ms of [250, 800, 1600, 2800]) setTimeout(status, ms); })
    .catch((x) => banner(x.message, true));
};
$("stop").addEventListener("click", () => musicDo("stop"));
$("next").addEventListener("click", () => {
  // The next song plays, even if this one was paused: show it at once
  // (the hold below would otherwise keep showing "paused", 11-15).
  if (music) { music.paused = false; drawMusic(); }
  musicDo("next");
});
// Play / pause flips on the spot; quick taps are folded into one command
// for where they end up, so the PC never gets pause-play-pause in a burst.
let toggleTimer, sentPaused = null;
$("play").addEventListener("click", async () => {
  if (music) {
    if (sentPaused === null) sentPaused = music.paused;
    music.paused = !music.paused;
    musicHold = Date.now() + 2500;
    drawMusic();
    clearTimeout(toggleTimer);
    toggleTimer = setTimeout(() => {
      const want = music && music.paused;
      if (want !== sentPaused) musicDo(want ? "pause" : "resume");
      sentPaused = null;
    }, 220);
    return;
  }
  const b = $("play"); b.disabled = true; b.classList.add("busy");
  try { await api("/api/play", { what: $("what").value }); banner(""); setTimeout(status, 1200); }
  catch (e) { banner("没放起来：" + e.message, true); }
  finally { b.disabled = false; b.classList.remove("busy"); }
});

let lists = [];
function whatText(arg) { return !arg || arg === "liked" ? "我喜欢的歌" : arg.startsWith("playlist:") ? "「" + arg.slice(9) + "」" : arg; }
function fillWhat(sel, keep) {
  sel.textContent = "";
  sel.append(new Option("我喜欢的歌", "liked"));
  for (const n of lists) sel.append(new Option(n, "playlist:" + n));
  if (keep && ![...sel.options].some((o) => o.value === keep)) sel.append(new Option(whatText(keep) + "（Husic 里没有了）", keep));
  if (keep) sel.value = keep;
  drawPicker(sel);
}

// The hidden <select> keeps the value; the picker is what the owner sees.
function drawPicker(sel) {
  const box = document.querySelector('.picker[data-for="' + sel.id + '"]');
  if (!box) return;
  let btn = box.querySelector(".pick-btn");
  if (!btn) {
    btn = el("button", "pick-btn"); btn.type = "button";
    btn.setAttribute("aria-haspopup", "listbox");
    btn.append(el("span"));
    btn.insertAdjacentHTML("beforeend", '<svg viewBox="0 0 12 12" aria-hidden="true"><path d="M3 4.5 6 7.5 9 4.5" /></svg>');
    btn.addEventListener("click", (ev) => { ev.stopPropagation(); box.classList.contains("open") ? closePickers() : openPicker(box, sel); });
    box.append(btn);
  }
  btn.firstChild.textContent = sel.selectedOptions[0]?.text || "";
}
function openPicker(box, sel) {
  closePickers();
  const menu = el("div", "menu"); menu.setAttribute("role", "listbox");
  [...sel.options].forEach((o, i) => {
    if (i === 1) { menu.append(el("hr")); menu.append(el("div", "lbl", "歌单")); }
    const b = el("button"); b.type = "button"; b.setAttribute("role", "option");
    b.setAttribute("aria-selected", String(o.value === sel.value));
    b.append(el("i", null, "✓"), el("span", null, o.text));
    b.addEventListener("click", (ev) => { ev.stopPropagation(); sel.value = o.value; drawPicker(sel); closePickers(); });
    menu.append(b);
  });
  box.classList.toggle("up", box.getBoundingClientRect().bottom + 320 > innerHeight && box.getBoundingClientRect().top > 320);
  box.append(menu); box.classList.add("open");
  menu.querySelector("[aria-selected=true]")?.focus();
}
function closePickers() {
  for (const b of document.querySelectorAll(".picker.open")) { b.classList.remove("open"); b.querySelector(".menu")?.remove(); }
}
document.addEventListener("click", closePickers);
document.addEventListener("keydown", (e) => { if (e.key === "Escape") closePickers(); });
async function playlists() {
  try { lists = await api("/api/playlists"); } catch { lists = []; }
  fillWhat($("what"), $("what").value || "liked");
}

// ---- 今天：刻度尺 + 定时 ----
function daysText(d, date) {
  if (date) {
    const n = Math.round((new Date(date + "T00:00") - new Date(new Date().toDateString())) / 864e5);
    return n === 0 ? "只今天" : n === 1 ? "只明天" : n < 0 ? "只一次 · 已过" : "只一次 · " + date.slice(5).replace("-", " 月 ") + " 日";
  }
  if (!d || !d.length || d.length === 7) return "每天";
  const s = [...d].sort().join("");
  if (s === "12345") return "工作日";
  if (s === "06") return "周末";
  return "周" + [...d].sort((a, b) => ((a + 6) % 7) - ((b + 6) % 7)).map((x) => dayName[x]).join("");
}
function actionText(e) { return e.action === "stop" ? "停止放歌" : e.action === "say" ? "说「" + e.arg + "」" : "放" + whatText(e.arg); }
function whenText(t) {
  const d = new Date(t), now = new Date();
  const n = Math.round((new Date(d.toDateString()) - new Date(now.toDateString())) / 864e5);
  return (n === 0 ? "今天" : n === 1 ? "明天" : "周" + dayName[d.getDay()]) + " " + hm(t);
}
const onToday = (e) => e.date ? e.date === isoDay(0) : !e.days || !e.days.length || e.days.includes(new Date().getDay());
function isoDay(plus) { const d = new Date(Date.now() + plus * 864e5); return d.getFullYear() + "-" + pad(d.getMonth() + 1) + "-" + pad(d.getDate()); }

let clocks = [], heardRuns = [], botReminders = [], remindersWired = false, editing = null;
async function schedules() {
  try { clocks = await api("/api/schedules"); } catch { return; }
  try { botReminders = await api("/api/reminders"); } catch { botReminders = []; }
  remindersWired = remindersWired || botReminders.length > 0;
  drawToday();
}
// Redrawn only when something on it changed (history polls every 4 s and
// a full redraw replayed the rows' entrance animation: the list flashed).
let todayKey = "", todayDrawn = false;
function drawToday() {
  const now = new Date();
  const key = JSON.stringify([clocks, botReminders, heardRuns.slice(0, 4), now.getHours(), now.getMinutes(), now.getDay()]);
  if (key === todayKey) return;
  todayKey = key;
  document.querySelector(".today").classList.toggle("drawn", todayDrawn);
  todayDrawn = true;
  const sorted = [...clocks].sort((a, b) => a.hour * 60 + a.minute - (b.hour * 60 + b.minute));
  const upcoming = clocks.filter((e) => !e.disabled && e.next).sort((a, b) => new Date(a.next) - new Date(b.next))[0];
  $("today-sub").textContent = "周" + dayName[now.getDay()] + (upcoming ? " · 下一个 " + whenText(upcoming.next) : "");

  const ol = $("schedules"); ol.textContent = "";
  if (!clocks.length) ol.append(el("li", "empty", "还没有定时"));
  for (const e of sorted) {
    const li = el("li", "alarm" + (e.disabled ? " off" : "") + (upcoming && e.id === upcoming.id ? " next" : ""));
    const what = el("div", "what");
    what.append(el("b", null, actionText(e)), el("span", null, daysText(e.days, e.date) + (upcoming && e.id === upcoming.id ? " · " + whenText(e.next) : "")));
    const sw = el("span", "switch"); sw.title = e.disabled ? "打开" : "关掉";
    const box = el("input"); box.type = "checkbox"; box.checked = !e.disabled; box.setAttribute("aria-label", "开关");
    box.addEventListener("click", (ev) => ev.stopPropagation());
    box.addEventListener("change", () => save({ ...e, disabled: !box.checked }, e.id).catch((x) => { box.checked = !box.checked; banner(x.message, true); }));
    sw.append(box, el("span"));
    li.append(el("span", "hm", pad(e.hour) + ":" + pad(e.minute)), what, sw);
    li.addEventListener("click", () => openEditor(e));
    ol.append(li);
  }
  // The bot's reminders for today, read-only: set and changed in
  // WeChat; Xiao Ai's own alarms are not readable, so the label says so.
  const rem = botReminders.filter((r) => new Date(r.at) > now);
  if (remindersWired) ol.append(el("li", "group-label", rem.length ? "今天的提醒" : "今天没有提醒了 · 小爱自己的闹钟这里看不到"));
  for (const r of rem) {
    const li = el("li", "alarm mijia");
    const what = el("div", "what");
    what.append(el("b", null, r.text), el("span", null, "提醒"));
    li.append(el("span", "hm", hm(r.at)), what, el("span"));
    ol.append(li);
  }
  // Xiao Ai runs our commands for the owner's voice and for Mijia automations
  // ("execute a text command") alike; the last few are listed under the clocks.
  const runs = husicWired ? heardRuns.slice(0, 4) : [];
  if (runs.length) {
    ol.append(el("li", "group-label", "经小爱执行的（口令或米家定时）"));
    for (const r of runs) {
      const li = el("li", "alarm mijia");
      const what = el("div", "what");
      what.append(el("b", null, r.action), el("span", null, (dayKey(r.at) === now.toDateString() ? "" : dayLabel(r.at) + " · ") + "「" + r.query + "」"));
      li.append(el("span", "hm", hm(r.at)), what, el("span"));
      ol.append(li);
    }
  }
}
// ---- 时间滚轮（iOS 式）：每格 40px，上下各垫两格让首尾也能停在中间 ----
const ROW = 40;
// Row i sits in the middle band when scrollTop = i*ROW + off (two pad rows
// above it; the wheel is 180 px, not five rows, so off is not 0).
const off = (box) => 2 * ROW + ROW / 2 - box.clientHeight / 2;
function buildWheel(box, n, step) {
  box.textContent = "";
  box.dataset.n = n;
  for (let k = 0; k < 2; k++) box.append(el("div", "pad"));
  for (let v = 0; v < n; v += step) { const d = el("div", null, pad(v)); d.dataset.v = v; box.append(d); }
  for (let k = 0; k < 2; k++) box.append(el("div", "pad"));
  let t;
  box.addEventListener("scroll", () => { mark(box); clearTimeout(t); t = setTimeout(() => mark(box), 80); }, { passive: true });
  box.addEventListener("keydown", (ev) => {
    if (ev.key === "ArrowUp" || ev.key === "ArrowDown") {
      ev.preventDefault();
      box.scrollBy({ top: ev.key === "ArrowUp" ? -ROW : ROW, behavior: "smooth" });
    } else if (/^[0-9]$/.test(ev.key) || ev.key === "Enter") {
      ev.preventDefault(); // the digit goes into the field once, not twice
      typeInto(box, /^[0-9]$/.test(ev.key) ? ev.key : "");
    }
  });
  // Mouse drag like a finger: the wheel follows the pointer, a flick carries
  // on and settles on a row. A click without a drag on the middle row types.
  let drag = null;
  box.addEventListener("pointerdown", (ev) => {
    if (ev.pointerType !== "mouse" || ev.button !== 0) return; // touch scrolls natively
    drag = { y: ev.clientY, top: box.scrollTop, moved: false, last: [[ev.clientY, performance.now()]] };
    box.setPointerCapture(ev.pointerId);
    box.style.scrollSnapType = "none";
  });
  box.addEventListener("pointermove", (ev) => {
    if (!drag) return;
    const dy = ev.clientY - drag.y;
    if (Math.abs(dy) > 3) drag.moved = true;
    box.scrollTop = drag.top - dy;
    drag.last.push([ev.clientY, performance.now()]);
    if (drag.last.length > 5) drag.last.shift();
  });
  const end = (ev) => {
    if (!drag) return;
    const d = drag; drag = null;
    box.style.scrollSnapType = "";
    if (!d.moved) {
      // Pointer capture makes the wheel the target: find the row under the pointer.
      box.releasePointerCapture?.(ev.pointerId);
      const row = document.elementFromPoint(ev.clientX, ev.clientY)?.closest?.("div[data-v]");
      if (row && +row.dataset.v === wheelValue(box)) typeInto(box, "");
      else if (row) setWheel(box, +row.dataset.v, true);
      return;
    }
    const [y0, t0] = d.last[0], [y1, t1] = d.last[d.last.length - 1];
    const v = t1 > t0 ? (y1 - y0) / (t1 - t0) : 0; // px per ms
    const target = Math.round((box.scrollTop - off(box) - v * 220) / ROW);
    const items = box.querySelectorAll("div[data-v]");
    box.scrollTo({ top: Math.max(0, Math.min(items.length - 1, target)) * ROW + off(box), behavior: "smooth" });
  };
  box.addEventListener("pointerup", end);
  box.addEventListener("pointercancel", end);
}

// typeInto puts a small field over the wheel's middle row: type the number,
// Enter or clicking away sets it, Esc leaves it as it was.
function typeInto(box, first) {
  if (box.parentElement.querySelector(".wheel-input[data-for='" + box.id + "']")) return;
  const n = +box.dataset.n;
  const input = el("input", "wheel-input");
  input.dataset.for = box.id;
  input.inputMode = "numeric";
  input.maxLength = 2;
  input.value = first || pad(wheelValue(box));
  input.style.left = box.offsetLeft + "px";
  input.style.width = box.offsetWidth + "px";
  box.parentElement.append(input);
  input.focus();
  if (first) input.setSelectionRange(1, 1); else input.select();
  let done = false;
  const finish = (keep) => {
    if (done) return; done = true;
    const v = parseInt(input.value, 10);
    if (keep && !isNaN(v)) setWheel(box, Math.max(0, Math.min(n - 1, v)), false); // jump: a smooth scroll can be re-snapped one row off
    input.remove();
    box.focus({ preventScroll: true });
  };
  input.addEventListener("input", () => {
    input.value = input.value.replace(/\D/g, "").slice(0, 2);
    if (input.value.length === 2) finish(true); // two digits: done, like a clock
  });
  input.addEventListener("keydown", (ev) => {
    if (ev.key === "Enter") { ev.preventDefault(); finish(true); }
    else if (ev.key === "Escape") { ev.preventDefault(); ev.stopPropagation(); finish(false); }
  });
  input.addEventListener("blur", () => finish(true));
}

function wheelValue(box) {
  const items = box.querySelectorAll("div[data-v]");
  const i = Math.max(0, Math.min(items.length - 1, Math.round((box.scrollTop - off(box)) / ROW)));
  return +items[i].dataset.v;
}
function mark(box) {
  const v = wheelValue(box);
  for (const d of box.querySelectorAll("div[data-v]")) d.classList.toggle("on", +d.dataset.v === v);
}
function setWheel(box, v, smooth) {
  const items = [...box.querySelectorAll("div[data-v]")];
  let i = items.findIndex((d) => +d.dataset.v >= v);
  if (i < 0) i = items.length - 1;
  box.scrollTo({ top: i * ROW + off(box), behavior: smooth ? "smooth" : "instant" });
  mark(box);
}
buildWheel($("e-hour"), 24, 1);
buildWheel($("e-minute"), 60, 1);

async function save(e, id) {
  const body = { hour: e.hour, minute: e.minute, days: e.date ? [] : e.days || [], date: e.date || "", action: e.action, arg: e.arg || "", disabled: !!e.disabled };
  if (id) await api("/api/schedules/" + id, body, "PUT"); else await api("/api/schedules", body);
  await schedules();
}

function setAction(a) {
  for (const b of $("e-action").children) b.setAttribute("aria-checked", String(b.dataset.a === a));
  $("e-what-row").hidden = a !== "play"; $("e-text-row").hidden = a !== "say";
}
function setRepeat(r) {
  for (const b of $("e-repeat").children) b.setAttribute("aria-checked", String(b.dataset.r === r));
  $("e-days").hidden = r !== "week";
}
const repeatOf = () => [...$("e-repeat").children].find((b) => b.getAttribute("aria-checked") === "true").dataset.r;
$("e-repeat").addEventListener("click", (ev) => { const r = ev.target.dataset?.r; if (r) setRepeat(r); });
function openEditor(e) {
  editing = e ? e.id : null;
  $("e-title").textContent = e ? "改定时" : "新增定时";
  const hh = e ? e.hour : 8, mm = e ? e.minute : 0;
  const days = e ? e.days || [] : [1, 2, 3, 4, 5];
  for (const b of $("e-days").children) b.setAttribute("aria-pressed", String(!days.length || days.includes(+b.dataset.d)));
  setRepeat(e && e.date ? (e.date === isoDay(1) ? "tomorrow" : "today") : "week");
  setAction(e ? e.action : husicWired ? "play" : "say");
  fillWhat($("e-what"), e && e.action === "play" ? e.arg || "liked" : "liked");
  $("e-text").value = e && e.action === "say" ? e.arg : "";
  $("e-delete").hidden = !e;
  $("editor").showModal();
  setWheel($("e-hour"), hh, false);
  setWheel($("e-minute"), mm, false);
  $("e-hour").focus({ preventScroll: true }); // arrow keys move the hour; no focus ring on 取消
}
$("add").addEventListener("click", () => openEditor(null));
$("e-cancel").addEventListener("click", () => $("editor").close());
$("editor").addEventListener("click", (ev) => { if (ev.target === $("editor")) $("editor").close(); });
$("e-days").addEventListener("click", (ev) => {
  const b = ev.target.closest("button"); if (!b) return;
  b.setAttribute("aria-pressed", String(b.getAttribute("aria-pressed") !== "true"));
});
$("e-action").addEventListener("click", (ev) => { const a = ev.target.dataset?.a; if (a) setAction(a); });
$("e-delete").addEventListener("click", async () => {
  if (!editing) return;
  try { await api("/api/schedules/" + editing, undefined, "DELETE"); $("editor").close(); await schedules(); }
  catch (e) { banner("没删掉：" + e.message, true); }
});
$("editor-form").addEventListener("submit", async (ev) => {
  ev.preventDefault();
  const h = wheelValue($("e-hour")), m = wheelValue($("e-minute"));
  const days = [...$("e-days").children].filter((b) => b.getAttribute("aria-pressed") === "true").map((b) => +b.dataset.d);
  const rep = repeatOf();
  const date = rep === "today" ? isoDay(0) : rep === "tomorrow" ? isoDay(1) : "";
  if (!date && !days.length) { banner("至少选一天", true); return; }
  const action = [...$("e-action").children].find((b) => b.getAttribute("aria-checked") === "true").dataset.a;
  const arg = action === "play" ? $("e-what").value : action === "say" ? $("e-text").value.trim() : "";
  if (action === "say" && !arg) { $("e-text").focus(); return; }
  const old = clocks.find((c) => c.id === editing);
  try {
    // A once entry that already ran was switched off; saving it again (a new
    // date or time) switches it back on.
    await save({ hour: h, minute: m, days: days.length === 7 ? [] : days, date, action, arg, disabled: date ? false : old ? old.disabled : false }, editing);
    $("editor").close(); banner("");
  } catch (e) { banner("没存上：" + e.message, true); }
});

// ---- 登录面板 ----
let started = false;
async function gate() {
  let a; try { a = await api("/api/auth"); } catch { a = { mode: "password", setup: false, logged_in: false }; }
  const inside = a.logged_in || a.mode === "header";
  $("gate").hidden = inside; $("app").hidden = !inside;
  document.querySelector(".lights").hidden = !inside; $("open-setup").hidden = !inside; $("dnd-btn").hidden = !inside;
  if (inside) return start();
  $("gate-code").hidden = !a.setup;
  $("gate-title").textContent = a.setup ? "设一个面板密码" : "登录面板";
  $("gate-tip").textContent = a.setup ? "第一次打开：填服务日志里的 6 位初始化码（docker compose logs 里搜 setup_code），再设一个至少 8 位的密码。" : "";
  $("gate-password").autocomplete = a.setup ? "new-password" : "current-password";
  $("gate-go").textContent = a.setup ? "设好，进入" : "登录";
  $("gate-form").dataset.setup = a.setup ? "1" : "";
  (a.setup ? $("gate-code") : $("gate-password")).focus();
}
$("gate-form").addEventListener("submit", async (ev) => {
  ev.preventDefault();
  const setup = !!$("gate-form").dataset.setup;
  $("gate-err").textContent = ""; $("gate-go").disabled = true;
  try {
    if (setup) await api("/api/auth/setup", { code: $("gate-code").value.trim(), password: $("gate-password").value });
    else await api("/api/auth/login", { password: $("gate-password").value });
    $("gate-password").value = "";
    await gate();
  } catch (e) { $("gate-err").textContent = e.message; }
  finally { $("gate-go").disabled = false; }
});

// ---- 设置：小米账号、选音箱、接入 ----
function tab(t) {
  for (const b of $("setup-tabs").children) b.setAttribute("aria-checked", String(b.dataset.t === t));
  for (const p of document.querySelectorAll(".pane")) p.hidden = p.dataset.pane !== t;
  if (t === "connect") connect();
}
$("setup-tabs").addEventListener("click", (e) => { const t = e.target.dataset?.t; if (t) tab(t); });
$("open-setup").addEventListener("click", () => { $("setup").showModal(); xiaomi(); });
$("setup-done").addEventListener("click", () => { $("setup").close(); status(); });
$("setup").addEventListener("click", (ev) => { if (ev.target === $("setup")) $("setup").close(); });

const loginWords = { ok: "已登录", renewing: "正在续期", "login-required": "登录失效了，请重新登录", "risk-control": "小米暂时限流，过两小时再试", none: "还没登录" };
async function xiaomi() {
  let x; try { x = await api("/api/xiaomi"); } catch { return; }
  const inAcct = x.login !== "none" && x.login !== "login-required";
  $("mi-login").hidden = inAcct; $("mi-done").hidden = !inAcct;
  if (!inAcct) { $("mi-err").textContent = x.login === "login-required" ? "登录失效了，请重新登录" : ""; return; }
  $("mi-state").textContent = "小米账号" + (loginWords[x.login] || x.login) + "。" + (x.speakers.length ? "选这个面板控制哪台音箱：" : "账号下没有找到音箱。");
  const box = $("mi-speakers"); box.textContent = "";
  for (const sp of x.speakers) {
    const row = el("button", "sheet-row pick"); row.type = "button";
    row.setAttribute("aria-checked", String(sp.id === x.chosen));
    row.append(el("span", null, sp.name), el("em", null, sp.model + (sp.online ? "" : " · 不在线")), el("i", null, "✓"));
    row.addEventListener("click", () => api("/api/xiaomi/speaker", { id: sp.id }).then(xiaomi).then(status).catch((e) => { $("mi-err").textContent = e.message; }));
    box.append(row);
  }
  box.hidden = !x.speakers.length;
  $("mi-err").textContent = x.error ? zh(x.error) : "";
}
let codeSent = false;
$("mi-go").addEventListener("click", async () => {
  $("mi-err").textContent = ""; $("mi-go").disabled = true;
  try {
    if (codeSent) {
      await api("/api/xiaomi/verify", { code: $("mi-code").value.trim() });
      codeSent = false; $("mi-code-row").hidden = true; $("mi-go").textContent = "登录小米";
      $("mi-password").value = ""; $("mi-code").value = "";
    } else {
      const r = await api("/api/xiaomi/login", { user: $("mi-user").value.trim(), password: $("mi-password").value });
      if (r.need_code) {
        codeSent = true; $("mi-code-row").hidden = false; $("mi-go").textContent = "提交验证码";
        $("mi-err").textContent = "小米发了验证码到绑定的手机（或邮箱），填进来。"; $("mi-code").focus();
        return;
      }
      $("mi-password").value = "";
    }
    await xiaomi(); status();
  } catch (e) { $("mi-err").textContent = e.message; }
  finally { $("mi-go").disabled = false; }
});
$("mi-form").addEventListener("submit", (e) => { e.preventDefault(); $("mi-go").click(); });
$("mi-test").addEventListener("click", async () => {
  const ol = $("test-steps"); ol.hidden = false; ol.textContent = "";
  ol.append(el("li", "wait", "音箱会念两句，等十来秒…"));
  $("mi-test").disabled = true;
  try {
    const steps = await api("/api/test", undefined, "POST");
    ol.textContent = "";
    for (const st of steps) {
      const li = el("li", st.ok ? "ok" : "bad");
      li.append(el("i", null, st.ok ? "✓" : "✗"), el("b", null, st.name));
      // Steps the service wrote in Chinese stay as they are (they may name
      // localhost); raw errors from below go through zh().
      if (st.detail) li.append(el("span", null, st.ok || /[\u4e00-\u9fff]/.test(st.detail) ? st.detail : zh(st.detail)));
      ol.append(li);
    }
    if (steps.every((x) => x.ok)) ol.append(el("li", "ok note", "都通了：刚才听到了两句就说明好了。"));
  } catch (e) { ol.textContent = ""; ol.append(el("li", "bad", e.message)); }
  finally { $("mi-test").disabled = false; }
});
$("mi-logout").addEventListener("click", async () => {
  if (!confirm("退出小米账号？音箱会停止工作，要重新登录才能用。")) return;
  try { await api("/api/xiaomi/logout", undefined, "POST"); await xiaomi(); status(); } catch (e) { $("mi-err").textContent = e.message; }
});

async function connect() {
  let c; try { c = await api("/api/connect"); } catch { return; }
  $("cc").textContent = "claude mcp add --transport http speaker " + c.mcp_url + ' --header "Authorization: Bearer ' + c.mcp_key + '"';
  $("codex").textContent = '[mcp_servers.speaker]\nurl = "' + c.mcp_url + '"\nhttp_headers = { Authorization = "Bearer ' + c.mcp_key + '" }';
  $("generic").textContent = JSON.stringify({ mcpServers: { speaker: { type: "http", url: c.mcp_url, headers: { Authorization: "Bearer " + c.mcp_key } } } }, null, 2);
  $("tray").textContent = "服务器地址：" + c.origin + "\n电脑钥匙：" + c.pc_key;
  if (document.activeElement !== $("base")) $("base").value = c.base || "";
}
document.querySelector('[data-pane="connect"]').addEventListener("click", async (e) => {
  const id = e.target.dataset?.copy; if (!id) return;
  try { await navigator.clipboard.writeText($(id).textContent); e.target.textContent = "已复制"; }
  catch { getSelection().selectAllChildren($(id)); e.target.textContent = "已选中，按 Ctrl+C"; }
  setTimeout(() => { e.target.textContent = "复制"; }, 1600);
});
$("base-save").addEventListener("click", () => api("/api/settings", { base: $("base").value.trim() })
  .then(() => { $("base-save").textContent = "已保存"; setTimeout(() => { $("base-save").textContent = "保存"; }, 1600); })
  .catch((e) => banner(e.message, true)));

// ---- 勿扰 ----
$("dnd-btn").addEventListener("click", async () => {
  if (dndOn) {
    try { const r = await api("/api/dnd", { on: false }); banner(r && r.held ? "勿扰关了，期间攒了 " + r.held + " 句" : ""); status(); history(); }
    catch (e) { banner("勿扰没关掉：" + e.message, true); }
    return;
  }
  $("dnd-sheet").showModal();
});
$("dnd-until").addEventListener("click", (ev) => {
  const u = ev.target.dataset?.u; if (!u) return;
  for (const b of $("dnd-until").children) b.setAttribute("aria-checked", String(b === ev.target));
  $("dnd-custom-row").hidden = u !== "custom";
});
$("dnd-cancel").addEventListener("click", () => $("dnd-sheet").close());
$("dnd-sheet").addEventListener("click", (ev) => { if (ev.target === $("dnd-sheet")) $("dnd-sheet").close(); });
$("dnd-form").addEventListener("submit", async (ev) => {
  ev.preventDefault();
  const pick = [...$("dnd-until").children].find((b) => b.getAttribute("aria-checked") === "true").dataset.u;
  const until = pick === "custom" ? $("dnd-time").value : pick;
  try { await api("/api/dnd", { on: true, until }); $("dnd-sheet").close(); status(); }
  catch (e) { banner("勿扰没打开：" + e.message, true); }
});

function start() {
  if (started) return;
  started = true;
  status(); history(); playlists(); schedules();
  // Every 2 s while music plays (the song name and play state follow quickly),
  // 5 s otherwise.
  (function tick() { setTimeout(() => { status().finally(tick); }, music ? 2000 : 5000); })();
  setInterval(history, 4000);
  setInterval(schedules, 60000);
  document.addEventListener("visibilitychange", () => { if (!document.hidden) { status(); history(); } });
  api("/api/status").then((s) => { if (s.login === "none") { $("setup").showModal(); xiaomi(); } }).catch(() => {});
}
gate();
