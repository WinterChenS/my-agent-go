// `tokens.files.mjs` 的类型声明（D1）。见该文件头部说明：没有 @types/node，
// 所以文件读取放在 .mjs 里，这里只补最小声明。

export interface ShellSourceFile {
  path: string;
  text: string;
}

export declare const FRONTEND_ROOT: string;
export declare const REPO_ROOT: string;
export declare function readContract(): { studioCss: string; indexCss: string };
export declare function shellSourceFiles(): ShellSourceFile[];
