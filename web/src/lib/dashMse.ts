/**
 * DASH（音画分轨的 fMP4）→ MediaSource：就是 B 站原站那一套。
 *
 * B 站 playurl 给的 dash 档位是**一条 `.m4s` 就是一个完整轨道**，文件头里写着分片索引：
 *
 *     ftyp 36 | moov 942（0–977，初始化段）| sidx 688（978–1665，分片表）| moof/mdat × N
 *
 * sidx 里是「每一片占多少字节、多少时长」（实测 `BV1NTp26eEkn` 480p：54 片 × 5.005 秒，
 * 合计 265.97 秒 = 4:27），而且 `startWithSap=1`——每片开头都是关键帧。
 * 所以正确放法是：Range 取头部拿索引 → 建 MediaSource → 视频、音频各一个 SourceBuffer →
 * 按索引一片片 append。原站抓包实测就是这么发请求的（`bytes=0-1665` → `1666-22170` → …，
 * 全部 206），`<video>.src` 是 `blob:`，起播只要几十 KB。
 *
 * 我们之前是反过来的：服务端 ffmpeg 把两条流合成整条 mp4 再交给 `<video>`，
 * 1080p 要等 15 秒、4K 要等几分钟（554 MB~1.77 GB）。这条路自己算索引、自己管缓冲，
 * 起播只要「头部 + 前两片」，换清晰度只重建视频那一个 SourceBuffer（音频轨不动、
 * 时间线连续），4K 也就不再需要等合流。
 *
 * 只负责「把字节喂进 <video>」：起播/缓冲/出错这些界面状态仍然由 VideoPlayer 管。
 */

/** 一片分片在文件里的位置与它在时间线上的起止（秒） */
interface Segment {
  offset: number;
  size: number;
  start: number;
  dur: number;
}

/** 一条轨道的索引 */
interface TrackIndex {
  /** 初始化段（ftyp+moov）的最后一个字节，append 时要原样子节 `0..initEnd` */
  initEnd: number;
  segments: Segment[];
  /** 这条轨道自己的总时长（秒），由索引算出来 */
  duration: number;
}

export interface DashTrack {
  /** 已经过 /api/stream 同源代理的地址 */
  url: string;
  /** SourceBuffer 的 MIME，例如 `video/mp4; codecs="av01.0.08M.08"` */
  mime: string;
}

export interface DashSpec {
  video: DashTrack;
  audio: DashTrack;
  /** 整条片子的时长（秒）；<=0 就用索引算出来的值 */
  durationSec?: number;
}

export interface DashHooks {
  /** 致命错误（Range 不被支持、append 被拒……）：交给页面退回服务端合流 */
  onFatal: (err: Error) => void;
  /**
   * 当前这条视频轨的第一片已经喂进去了（画面即将出来）。
   *
   * 上层要用它来「重新催一次 play()」：换到 dash 之前那一次 play() 常常提前发生
   * （`el.src` 是异步设的——先 probe 两条轨，再建 MediaSource），
   * 那时 play() 作用于**旧片源**、promise 还照样 resolve，于是新管线挂上去以后没人再催，
   * 画面就停在那儿（实测：切到 dash 后 paused=true、readyState=4，怎么等都不动）。
   */
  onReady?: () => void;
  /** 诊断用：喂了几片、走到哪一秒 */
  onProgress?: (info: { track: 'video' | 'audio'; appended: number; bufferedAhead: number }) => void;
}

/** 头部窗口：moov+sidx 在小片子里 1.6 KB，两小时的片子 sidx 约 17 KB，64 KB 一次拿全 */
const HEADER_WINDOW = 64 * 1024;
/** 往下预取多少秒：一片 5 秒，等于同时最多三四片在飞 */
const AHEAD_SEC = 20;
/** 身后留多少秒（再往前的分片就从 SourceBuffer 里删掉，4K 才不会把整条都拉下来） */
const KEEP_BEHIND_SEC = 30;
/** SourceBuffer 操作用超时兜底：`updateend` 偶尔不来，不能把整条管道挂死 */
const SB_TIMEOUT_MS = 15000;

/**
 * 编码族（`avc1.640033` → `avc1`）。同族换档只需要 changeType；
 * 跨族（AV1↔H.264）才必须重建 SourceBuffer。
 */
function family(mime: string): string {
  const m = /codecs="?([^";]+)/i.exec(mime);
  const codec = (m ? m[1] : '').trim().toLowerCase();
  if (codec.startsWith('avc')) return 'avc1';
  if (codec.startsWith('hvc') || codec.startsWith('hev')) return 'hevc';
  if (codec.startsWith('av01')) return 'av01';
  if (codec.startsWith('vp09') || codec.startsWith('vp8')) return 'vp9';
  return codec;
}

function once(target: EventTarget, event: string, timeoutMs: number): Promise<void> {
  return new Promise((resolve, reject) => {
    const timer = window.setTimeout(() => {
      target.removeEventListener(event, done);
      target.removeEventListener('error', fail);
      reject(new Error(`${event} 超时`));
    }, timeoutMs);
    const done = () => {
      window.clearTimeout(timer);
      target.removeEventListener('error', fail);
      resolve();
    };
    const fail = () => {
      window.clearTimeout(timer);
      target.removeEventListener(event, done);
      reject(new Error('SourceBuffer/MediaSource 报错'));
    };
    target.addEventListener(event, done, { once: true });
    target.addEventListener('error', fail, { once: true });
  });
}

/**
 * 按字节区间取一段（必须 206：如果上游把 Range 当耳边风、回了 200 整条文件，宁可失败）。
 *
 * `signal` 是拖动进度条时的那把刹车：4K 一片 1~1.4 MB，用户拖到别处以后这一片已经没有用了，
 * 让它拉完再丢掉，等于每拖一次都要先白等一秒多（实测见 #seeked 的注释）。
 */
async function rangeFetch(
  url: string,
  start: number,
  end: number,
  signal?: AbortSignal,
): Promise<ArrayBuffer> {
  const res = await fetch(url, { headers: { Range: `bytes=${start}-${end}` }, signal });
  if (res.status !== 206) {
    throw new Error(`上游不支持 Range（HTTP ${String(res.status)}）`);
  }
  const buf = await res.arrayBuffer();
  const want = end - start + 1;
  if (buf.byteLength !== want) {
    throw new Error(`Range 长度不对：要 ${String(want)} 拿到 ${String(buf.byteLength)}`);
  }
  return buf;
}

interface Box {
  type: string;
  offset: number;
  size: number;
}

/** 走一遍顶层 box（64 位 size 也认） */
function walkBoxes(bytes: Uint8Array): Box[] {
  const view = new DataView(bytes.buffer, bytes.byteOffset, bytes.byteLength);
  const out: Box[] = [];
  let at = 0;
  while (at + 8 <= bytes.byteLength) {
    let size = view.getUint32(at);
    const type = String.fromCharCode(bytes[at + 4]!, bytes[at + 5]!, bytes[at + 6]!, bytes[at + 7]!);
    let header = 8;
    if (size === 1) {
      if (at + 16 > bytes.byteLength) break;
      size = Number(view.getBigUint64(at + 8));
      header = 16;
    } else if (size === 0) {
      size = bytes.byteLength - at;
    }
    if (size < header) break;
    out.push({ type, offset: at, size });
    at += size;
  }
  return out;
}

/** 解 sidx（version 0/1，reference type 只认 media） */
function parseSidx(bytes: Uint8Array, box: Box): TrackIndex {
  const view = new DataView(bytes.buffer, bytes.byteOffset, bytes.byteLength);
  const base = box.offset;
  const version = bytes[base + 8]!;
  const timescale = view.getUint32(base + 16);
  let at: number;
  let earliest = 0;
  let firstOffset = 0;
  if (version === 0) {
    earliest = view.getUint32(base + 20);
    firstOffset = view.getUint32(base + 24);
    at = base + 28;
  } else {
    earliest = Number(view.getBigUint64(base + 20));
    firstOffset = Number(view.getBigUint64(base + 28));
    at = base + 36;
  }
  at += 2; // reserved
  const count = view.getUint16(at);
  at += 2;
  const segments: Segment[] = [];
  let pos = box.offset + box.size + firstOffset;
  let ticks = 0;
  for (let i = 0; i < count; i += 1) {
    if (at + 12 > bytes.byteLength) break;
    const w0 = view.getUint32(at);
    const durTicks = view.getUint32(at + 4);
    at += 12;
    if ((w0 & 0x80000000) !== 0) continue; // 非媒体引用（理论上有，实际没有）
    const size = w0 & 0x7fffffff;
    segments.push({
      offset: pos,
      size,
      start: (earliest + ticks) / timescale,
      dur: durTicks / timescale,
    });
    pos += size;
    ticks += durTicks;
  }
  const duration = (earliest + ticks) / timescale;
  const moov = walkBoxes(bytes).find((b) => b.type === 'moov');
  if (!moov) throw new Error('这条轨道没有 moov，不是 fMP4');
  return { initEnd: moov.offset + moov.size - 1, segments, duration };
}

/** 取头部并把分片索引解出来（sidx 超出窗口时按它自己的位置再补一次） */
async function probe(url: string): Promise<TrackIndex> {
  const head = new Uint8Array(await rangeFetch(url, 0, HEADER_WINDOW - 1));
  const boxes = walkBoxes(head);
  const moov = boxes.find((b) => b.type === 'moov');
  if (!moov) throw new Error('轨道头部里没有 moov');
  const sidx = boxes.find((b) => b.type === 'sidx');
  if (!sidx) throw new Error('轨道头部里没有 sidx（不是可分段取流的 fMP4）');
  const fit = sidx.offset + sidx.size <= head.byteLength;
  const bytes = fit
    ? head
    : new Uint8Array(await rangeFetch(url, 0, sidx.offset + sidx.size - 1));
  return parseSidx(bytes, sidx);
}

/** 找出「覆盖 at 这一秒」的分片序号（宁可早一片，别漏帧） */
function segmentAt(segments: readonly Segment[], at: number): number {
  let lo = 0;
  let hi = segments.length - 1;
  let found = 0;
  while (lo <= hi) {
    const mid = (lo + hi) >> 1;
    const seg = segments[mid]!;
    if (seg.start <= at + 0.05) {
      found = mid;
      lo = mid + 1;
    } else {
      hi = mid - 1;
    }
  }
  return found;
}

interface TrackState {
  kind: 'video' | 'audio';
  track: DashTrack;
  index: TrackIndex;
  sb: SourceBuffer | null;
  /** 下一个要 append 的分片序号 */
  next: number;
  /**
   * 取片代次。每次 seek/换档 +1。
   *
   * 它治的是「拖动进度条把播放拖死」那个 bug：`#appendOne` 先读 `next` 再 await 取分片，
   * 回来以后无条件 `next += 1`。用户在这段等待里拖了进度条，`#seeked` 刚把 `next` 摆到
   * 目标那一片，飞在半路的旧请求一回手就把它加过一位——**覆盖播放位置的那一片从此没人取**，
   * 缓冲从目标点往后 5 秒才开始，`seeking` 永远不结束（实测 30 秒仍 rs=1、currentTime 不动，
   * 只能刷新页面）。带上代次以后，回来的旧分片直接丢掉，不碰 `next`。
   */
  gen: number;
  /** 正在飞的取片请求（seek 时 abort 掉，不用等它把 1.4 MB 拉完） */
  abort: AbortController | null;
  appended: number;
}

export class DashMsePlayer {
  readonly el: HTMLVideoElement;
  /** 音频轨地址：同一个音频轨 + 同一个 <video> 才能「只换视频轨」地切清晰度 */
  readonly audioUrl: string;

  #spec: DashSpec;
  #hooks: DashHooks;
  #ms: MediaSource | null = null;
  #objUrl = '';
  #video: TrackState | null = null;
  #audio: TrackState | null = null;
  #destroyed = false;
  #pumping = false;
  /** 泵在跑的时候又被叫了一次（seek/换档），收尾后补跑一轮 */
  #pumpAgain = false;
  #starting: Promise<void> | null = null;
  #onSeeking = (): void => this.#seeked();
  #onTimeUpdate = (): void => this.#pump();
  #lastSwitchAt = 0;

  constructor(el: HTMLVideoElement, spec: DashSpec, hooks: DashHooks) {
    this.el = el;
    this.#spec = spec;
    this.#hooks = hooks;
    this.audioUrl = spec.audio.url;
  }

  /** 起播：取索引 → 建 MediaSource → append 初始化段和起始分片 */
  start(atSec = 0): Promise<void> {
    if (this.#starting === null) this.#starting = this.#start(atSec);
    return this.#starting;
  }

  async #start(atSec: number): Promise<void> {
    const [videoIndex, audioIndex] = await Promise.all([
      probe(this.#spec.video.url),
      probe(this.#spec.audio.url),
    ]);
    if (this.#destroyed) return;
    const ms = new MediaSource();
    this.#ms = ms;
    this.#objUrl = URL.createObjectURL(ms);
    this.el.src = this.#objUrl;
    await once(ms, 'sourceopen', 10000);
    if (this.#destroyed) return;
    const total = this.#spec.durationSec && this.#spec.durationSec > 0
      ? this.#spec.durationSec
      : Math.max(videoIndex.duration, audioIndex.duration);
    if (Number.isFinite(total) && total > 0) ms.duration = total;

    // 两条轨道各自一个 SourceBuffer：这就是「换清晰度只换一路」的前提
    const vState: TrackState = {
      kind: 'video',
      track: this.#spec.video,
      index: videoIndex,
      sb: ms.addSourceBuffer(this.#spec.video.mime),
      next: segmentAt(videoIndex.segments, atSec),
      gen: 0,
      abort: null,
      appended: 0,
    };
    const aState: TrackState = {
      kind: 'audio',
      track: this.#spec.audio,
      index: audioIndex,
      sb: ms.addSourceBuffer(this.#spec.audio.mime),
      next: segmentAt(audioIndex.segments, atSec),
      gen: 0,
      abort: null,
      appended: 0,
    };
    this.#video = vState;
    this.#audio = aState;

    await this.#appendInit(vState);
    await this.#appendInit(aState);
    // 起播位置不在开头时，先把那一片喂进去，再把进度摆过去：反过来的话 seek 会被夹回 0
    await this.#appendOne(vState);
    await this.#appendOne(aState);
    if (atSec > 0.5) {
      const seg = videoIndex.segments[vState.next - 1];
      this.el.currentTime = seg ? Math.max(seg.start + 0.01, atSec) : atSec;
    }
    this.el.addEventListener('seeking', this.#onSeeking);
    this.el.addEventListener('timeupdate', this.#onTimeUpdate);
    this.#pump();
    this.#hooks.onReady?.();
  }

  /**
   * 换清晰度：只动视频那一路，音频轨和时间线都不动。
   * 原站「秒切」就是这个道理：不用重新拿整条文件，也不用等合流。
   *
   * 代价只有「新一档的初始化段 + 当前那一片」：1080p 约 27 KB，4K 约 200 KB。
   */
  async switchVideo(track: DashTrack): Promise<void> {
    const ms = this.#ms;
    const old = this.#video;
    if (!ms || ms.readyState !== 'open' || !old) throw new Error('MediaSource 还没就绪');
    const index = await probe(track.url);
    if (this.#destroyed || this.#ms !== ms) return;
    const at = this.el.currentTime;
    // 先把视频轨从状态里摘掉，pump 就不会再往旧 SourceBuffer 里塞东西；
    // 不摘就动手会撞上「SourceBuffer 正在 updating」——removeSourceBuffer 直接抛 InvalidStateError
    this.#video = null;

    // 原则：**尽量不新建 SourceBuffer**。B 站同族档位（AV1 各档之间）codec 串只差 level，
    // 同 MIME 就直接复用，只差 level 就 changeType。真新建会撞 Chromium 的
    // `addSourceBuffer: reached the limit of SourceBuffer objects`（实测），一撞就退回服务端合流。
    // 先把旧 SourceBuffer 从状态里摘干净（pump 手里那份引用也要跟着失效），再去动它
    const oldSb = old.sb;
    old.sb = null;
    // 旧档位还在飞的那一片一并作废：换档多半是「画面卡了，换一档试试」，
    // 让它把上一条流剩下的字节拉完纯属白等
    old.gen += 1;
    const pending = old.abort;
    old.abort = null;
    pending?.abort();
    let sb = oldSb;
    if (sb !== null && family(old.track.mime) !== family(track.mime)) {
      // 跨编码族（AV1↔H.264）：只能摘掉重建，重建失败就让上层回退合流
      await this.#idle(sb);
      try {
        ms.removeSourceBuffer(sb);
      } catch (err) {
        console.warn('[dash] removeSourceBuffer 失败：', String(err));
      }
      sb = null;
    } else if (sb !== null && old.track.mime !== track.mime) {
      // 同族、只是 level/分辨率变了（AV1 1080p → AV1 4K 正是这种）：用 changeType 改 MIME，
      // **不新建** SourceBuffer。新建代价很大：摘掉再建这条路在 Chromium 上会撞
      // `addSourceBuffer: reached the limit of SourceBuffer objects`（实测），
      // 一撞就退回服务端合流——4K 那一档要等的是 128 MB 的合流文件。
      const anySb = sb as SourceBuffer & { changeType?: (mime: string) => void };
      let changed = false;
      if (typeof anySb.changeType === 'function') {
        try {
          await this.#idle(sb);
          anySb.changeType(track.mime);
          await this.#idle(sb);
          changed = true;
        } catch (err) {
          console.warn('[dash] changeType 没成，改为重建视频 SourceBuffer：', String(err));
        }
      }
      if (!changed) {
        await this.#idle(sb);
        try {
          ms.removeSourceBuffer(sb);
        } catch (err) {
          console.warn('[dash] removeSourceBuffer 失败：', String(err));
        }
        sb = null;
      }
    }
    if (sb === null) sb = ms.addSourceBuffer(track.mime);
    else await this.#clearAhead(sb, at);

    const state: TrackState = {
      kind: 'video',
      track,
      index,
      sb,
      next: segmentAt(index.segments, at),
      gen: 0,
      abort: null,
      appended: 0,
    };
    this.#video = state;
    this.#lastSwitchAt = performance.now();
    // 新一档的初始化段（参数集、分辨率都在里面）先喂，再立刻喂当前这一片：画面几百毫秒内就换过来
    await this.#appendInit(state);
    await this.#appendOne(state);
    void this.#pump();
    this.#hooks.onReady?.();
  }

  destroy(): void {
    this.#destroyed = true;
    this.el.removeEventListener('seeking', this.#onSeeking);
    this.el.removeEventListener('timeupdate', this.#onTimeUpdate);
    const ms = this.#ms;
    this.#ms = null;
    this.#video = null;
    this.#audio = null;
    if (ms) {
      // endOfStream：不然摘掉 src 之后 MediaSource 会一直挂在 open/closed 之间
      try {
        if (ms.readyState === 'open') ms.endOfStream();
      } catch {
        /* 已经结束/关闭就算了 */
      }
    }
    if (this.#objUrl) {
      URL.revokeObjectURL(this.#objUrl);
      this.#objUrl = '';
    }
    if (this.el.src.startsWith('blob:')) {
      this.el.removeAttribute('src');
      this.el.load();
    }
  }

  /** 换档后多久才出画面（诊断用，页面据此判断要不要盖加载态） */
  get lastSwitchAt(): number {
    return this.#lastSwitchAt;
  }

  async #appendInit(t: TrackState): Promise<void> {
    const buf = await rangeFetch(t.track.url, 0, t.index.initEnd);
    await this.#append(t, buf);
  }

  async #appendOne(t: TrackState): Promise<boolean> {
    const seg = t.index.segments[t.next];
    if (!seg) return false;
    const sb = t.sb;
    if (!sb) return false;
    // 整片都已经在缓冲里（拖回看过的位置）就别再下一遍。
    // 注意要按**整片**判断：只看起点的话，刚清掉播放位置之后那一段时，跨过播放位置的那一片
    // 会被误判成「已经有了」而跳过，playhead 前面就永远差一片，画面卡住。
    if (this.#covered(sb, seg.start, seg.start + seg.dur)) {
      t.next += 1;
      return true;
    }
    const gen = t.gen;
    const ctrl = new AbortController();
    t.abort = ctrl;
    let buf: ArrayBuffer;
    try {
      buf = await rangeFetch(t.track.url, seg.offset, seg.offset + seg.size - 1, ctrl.signal);
    } catch (err) {
      // seek 把这一片掐了：不是错误，交给新的泵从目标点重新取
      if (ctrl.signal.aborted) return false;
      throw err;
    } finally {
      if (t.abort === ctrl) t.abort = null;
    }
    // 字节还没到手时轨道可能已经换了（用户点了别的清晰度），或者用户把进度条拖走了：
    // 两种情况都不能动 `next`——拖走时 `#seeked` 已经把它摆到目标那一片，
    // 这里再 `+= 1` 就正好跳过「覆盖播放位置」的那一片，画面从此卡死在目标点上。
    if (this.#destroyed || t.sb !== sb || t.gen !== gen) return false;
    await this.#append(t, buf);
    if (t.gen !== gen) return false;
    t.next += 1;
    t.appended += 1;
    this.#hooks.onProgress?.({
      track: t.kind,
      appended: t.appended,
      bufferedAhead: this.#ahead(sb),
    });
    return true;
  }

  async #append(t: TrackState, buf: ArrayBuffer): Promise<void> {
    const sb = t.sb;
    if (!sb || this.#destroyed) return;
    await this.#idle(sb);
    if (!sb.updating) {
      sb.appendBuffer(buf);
      await once(sb, 'updateend', SB_TIMEOUT_MS);
    }
  }

  /**
   * 换档时把**播放位置之后**的旧画面清掉（之前的留着：那一截已经放过了，清掉反而黑一下）。
   * 不清的话新旧两档的画面在同一段时间轴上交叠，浏览器会混着解码，出现花屏/顿一下。
   */
  async #clearAhead(sb: SourceBuffer, at: number): Promise<void> {
    await this.#idle(sb);
    const ranges = sb.buffered;
    if (ranges.length === 0) return;
    const end = ranges.end(ranges.length - 1);
    if (end <= at + 0.2) return;
    try {
      sb.remove(Math.max(at, ranges.start(0)), end);
      await once(sb, 'updateend', SB_TIMEOUT_MS);
    } catch {
      /* 清不掉就把新数据往后叠，别为此中断播放 */
    }
  }

  /** 这一段时间是否已经完整躺在缓冲里了（拖回看过的位置时别再下一遍） */
  #covered(sb: SourceBuffer, from: number, to: number): boolean {
    const ranges = sb.buffered;
    for (let i = 0; i < ranges.length; i += 1) {
      if (ranges.start(i) <= from + 0.15 && ranges.end(i) >= to - 0.15) return true;
    }
    return false;
  }

  async #idle(sb: SourceBuffer): Promise<void> {
    if (!sb.updating) return;
    await once(sb, 'updateend', SB_TIMEOUT_MS);
  }

  /** 当前播放位置往前还垫了多少秒（没有覆盖当前时间的缓冲就算 0） */
  #ahead(sb: SourceBuffer): number {
    const at = this.el.currentTime;
    const ranges = sb.buffered;
    for (let i = 0; i < ranges.length; i += 1) {
      if (ranges.start(i) <= at + 0.3 && ranges.end(i) > at) return ranges.end(i) - at;
    }
    return 0;
  }

  /**
   * 缓冲区往前攒够 AHEAD_SEC 就停手；顺手把身后老掉的分片删掉。
   *
   * 两条轨**并行**填：以前是「视频填满 20 秒才轮到声音」，4K 一片 1~1.4 MB，
   * 拖动进度条之后要先等视频那 4 片一片片排队取完，声音才开始动——
   * 实测（`BV1...`/2160p AV1，本地端口 18081）单次冷 seek 恢复播放 0.4~2.0 秒，
   * 其中一半以上时间花在排在后面的音频轨上。并行以后这段时间基本消失。
   */
  #pump(): void {
    if (this.#destroyed) return;
    // 已经有泵在跑：记一笔，等它收尾后再来一轮。
    // 拖动进度条时这一笔是必需的——seek 会 abort 掉在飞的分片，那一轮泵随即空转结束，
    // 而「站在目标点上、缓冲里没有这一片」的状态不会再触发 timeupdate，
    // 没人再叫泵就是彻底不动了（用户看到的「卡住」）。
    if (this.#pumping) {
      this.#pumpAgain = true;
      return;
    }
    this.#pumping = true;
    void (async () => {
      try {
        await Promise.all([this.#fillTrack(this.#video), this.#fillTrack(this.#audio)]);
      } catch (err) {
        this.#fatal(err);
      } finally {
        this.#pumping = false;
        if (this.#pumpAgain && !this.#destroyed) {
          this.#pumpAgain = false;
          this.#pump();
        }
      }
    })();
  }

  /** 一条轨往前填到 AHEAD_SEC，再把身后老掉的分片删掉 */
  async #fillTrack(t: TrackState | null): Promise<void> {
    if (!t) return;
    // 手里这份 SourceBuffer 引用要一直核：换档会把 `t.sb` 摘成 null 再换一个新的，
    // 这里若拿 `t.sb` 现读就会读到 null（`null.buffered` 直接抛，整条管道判死退回合流）
    const sb = t.sb;
    if (!sb) return;
    this.#realign(t);
    let guard = 0;
    while (!this.#destroyed && guard < 60) {
      guard += 1;
      if (t.sb !== sb) return;
      if (this.#ahead(sb) >= AHEAD_SEC) break;
      if (!(await this.#appendOne(t))) break;
    }
    await this.#pruneBehind(t);
  }

  /**
   * 兜底：播放位置那一片不在缓冲里、而 `next` 已经跑到它后面去了 —— 把 `next` 拨回来。
   *
   * 主路（`#seeked` 摆 next + 代次保护）已经不会跳片了，这一条是给「万一」准备的：
   * 只要判错一次，用户看到的就是「拖到哪儿都卡死，只能刷新页面」，代价太大。
   * 跳到未来的那一段缓冲不会丢，只是被晾着，`#pruneBehind` 会顺手收掉。
   */
  #realign(t: TrackState): void {
    const sb = t.sb;
    if (!sb) return;
    const at = this.el.currentTime;
    for (let i = 0; i < sb.buffered.length; i += 1) {
      if (sb.buffered.start(i) <= at + 0.15 && sb.buffered.end(i) > at) return;
    }
    t.next = segmentAt(t.index.segments, at);
  }

  async #pruneBehind(t: TrackState): Promise<void> {
    const sb = t.sb;
    if (!sb || sb.buffered.length === 0) return;
    const cut = this.el.currentTime - KEEP_BEHIND_SEC;
    if (cut <= 1 || sb.buffered.start(0) > cut - 1) return;
    await this.#idle(sb);
    if (this.#destroyed || sb.updating) return;
    try {
      sb.remove(0, cut);
      await once(sb, 'updateend', SB_TIMEOUT_MS);
    } catch {
      /* 删缓冲失败不影响播放 */
    }
  }

  /**
   * 拖动进度条：两条轨都重新对齐到目标时刻那一片。
   *
   * 拖进度条不是「一次 seek」而是几十次（滑块每次 pointermove 都改 currentTime），
   * 所以这里做的两件事都必须便宜、且不能留下后患：
   *   1. `gen += 1` + abort：飞在半路的分片立刻作废（4K 一片 1~1.4 MB，
   *      等它拉完再丢掉，每拖一次要多等一秒多）；
   *   2. 重设 `next`，然后重新叫泵。
   */
  #seeked(): void {
    if (this.#destroyed) return;
    const at = this.el.currentTime;
    for (const t of [this.#video, this.#audio]) {
      if (!t || !t.sb) continue;
      t.gen += 1;
      const ctrl = t.abort;
      t.abort = null;
      ctrl?.abort();
      t.next = segmentAt(t.index.segments, at);
    }
    this.#pump();
  }

  #fatal(err: unknown): void {
    if (this.#destroyed) return;
    this.#destroyed = true;
    const e = err instanceof Error ? err : new Error(String(err));
    console.warn('[dash] 拼流中断：', e.message);
    try {
      this.destroy();
    } catch {
      /* 清理失败不掩盖原始错误 */
    }
    this.#hooks.onFatal(e);
  }
}

/**
 * 把编码串压成 MSE 认的标准形式。
 *
 * B 站给的 AV1 是 `av01.0.08M.08.0.110.01.01.01.0`——后面那串是它自己加的编码配置参数，
 * 不是 RFC 6381 的一部分。Chromium 两种都认，但别的浏览器只认 `av01.P.LLT.XX` 这种四段式，
 * 所以统一在这里收一刀，`isTypeSupported` 和 SourceBuffer 的 MIME 都用同一份结果。
 */
export function mseCodec(codec: string): string {
  const parts = codec.trim().split('.');
  const family = (parts[0] ?? '').toLowerCase();
  if (family === 'av01') return parts.slice(0, 4).join('.');
  if (family === 'avc1' || family === 'avc3') return parts.slice(0, 2).join('.');
  if (family === 'hvc1' || family === 'hev1') return parts.slice(0, 5).join('.');
  if (family === 'vp09') return parts.slice(0, 4).join('.');
  return codec.trim();
}

/**
 * 这台浏览器能不能自己拼 DASH（MSE + 这一档的编码）。
 * AV1/HEVC 的 MSE 支持因浏览器而异（Safari 到现在也没有 AV1-MSE），不支持就走服务端合流。
 */
export function canPlayDash(videoCodec: string, audioCodec = 'mp4a.40.2'): boolean {
  if (typeof window === 'undefined' || typeof window.MediaSource === 'undefined') return false;
  try {
    return (
      MediaSource.isTypeSupported(`video/mp4; codecs="${mseCodec(videoCodec)}"`) &&
      MediaSource.isTypeSupported(`audio/mp4; codecs="${mseCodec(audioCodec)}"`)
    );
  } catch {
    return false;
  }
}
