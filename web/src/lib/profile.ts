import type { ProfileField } from './types';

/** 资料行的展示口径：站上的 label/value 原样躺在库里，这里只换「说法」，不动「事实」。
 *
 *  所以改口径不用重扫一遍艺人——已经同步下来的也立刻跟着变。跟「身材」那种走抓取期的
 *  规整不同：那是模型层的地盘（`body_json` 存 4 个数字，接口只发数字），这里是页面用语。
 *  写法和图标跟 https://github.com/JinsFoni/peach 的资料卡对齐（用户点名要那一套）。 */

/** 站上的中文标签 → 资料卡上的写法。表里没有的照站上原文列——那是站上确有的说法，丢掉就少一项。 */
const LABELS: Record<string, string> = {
  感情状态: '感情',
  喜欢: '取向',
  假奶子: '胸部',
  是否有穿环: '穿环',
  兴趣爱好: '爱好',
};

/** 这几项站上写「是／否」，英文站写 `Yes`／`No`，换成页面用语。
 *  胸部那两项是反着来的：`否`＝没动过＝天然，`是`＝动过＝假奶。 */
const FLAGS: Record<string, Record<string, string>> = {
  胸部: { 是: '假奶', 否: '天然', yes: '假奶', no: '天然' },
  纹身: { 是: '有', 否: '无', yes: '有', no: '无' },
  穿环: { 是: '有', 否: '无', yes: '有', no: '无' },
};

/** `111lbs. (50kg)` 里的公斤数；只有磅时按 0.45359237 换算。 */
const KG = /(\d{2,3})\s*kg\b/i;
const LB = /(\d{2,3}(?:\.\d+)?)\s*lbs?\b/i;

function weight(text: string): string {
  const kg = KG.exec(text);
  if (kg) return `${kg[1]} kg`;
  const lb = LB.exec(text);
  if (lb) return `${Math.round(Number(lb[1]) * 0.45359237)} kg`;
  return text;
}

/** 资料行 → 页面上的资料行（标签改口径、布尔值本地化、体重只留公斤）。 */
export function displayProfile(fields: ProfileField[]): ProfileField[] {
  return fields.map((field) => {
    const raw = field.label.trim();
    const label = LABELS[raw] ?? raw;
    const value = field.value.trim();
    const flag = FLAGS[label]?.[value.toLowerCase()];
    return { label, value: flag ?? (label === '体重' ? weight(value) : field.value) };
  });
}
