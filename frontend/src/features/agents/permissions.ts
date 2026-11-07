// 文件系统权限的宽严序（U03 / UI07）。
//
// 与 Go 侧 `config.NormalizeFilesystem` + `config.Merge` 的收紧语义必须一致：
// 界面用这张表禁用"超过上限"的选项，后端用同一个语义拒绝越界草稿。
// 两边分叉的后果不是报错，而是"UI 说可以、Merge 静默夹回"的静默降级。
//
// 单独成文件（而不是塞进 api.ts）是为了让组件与测试都不必 mock 绑定层
// 才能拿到这条纯规则。

export const FILESYSTEM_VALUES = ["none", "read", "workspace", "unrestricted"] as const;

export type FilesystemValue = (typeof FILESYSTEM_VALUES)[number];

/** 宽严序：数字越大越宽。不认识的值按最严处理（与 Go 的安全默认一致）。 */
export function filesystemRank(value: string): number {
  switch (value) {
    case "unrestricted":
      return 3;
    case "workspace":
      return 2;
    case "read":
      return 1;
    case "none":
      return 0;
    default:
      return 0;
  }
}

/** 草稿是否不比上限更宽（true = 允许）。 */
export function filesystemAtMost(draft: string, ceiling: string): boolean {
  return filesystemRank(draft) <= filesystemRank(ceiling);
}
