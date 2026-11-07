// i18n 对外入口（U00）：消息表 + 格式化。
//
// 内部拆成 catalog.ts（消息表与 t）与 format.ts（Intl 格式化），
// 是为了避免 format 与 t 互相 import 形成环：格式化里的单位符号也来自 catalog。
export * from "./catalog";
export * from "./format";
