// scripts/check-ui-contract.mjs 的类型声明（U00）。
//
// 这个脚本刻意是纯 JS：它要能在 `node scripts/check-ui-contract.mjs`
// 直接跑（不经过打包），也要能被前端测试 import 来做同一份判定。
// 声明文件让 typecheck 阶段的 vitest 用例能用上导出，而不必给整个仓库
// 引入 @types/node（那会改 lock 文件，超出 U00 范围）。

export interface CopyViolation {
  file: string;
  line: number;
  rule: string;
  text: string;
}

export interface ContractIssue {
  kind: string;
  file: string;
  detail?: string;
  locale?: string;
}

export interface BaselineFile {
  files: Record<string, number>;
  note?: string;
}

export interface ContractResult {
  ok: boolean;
  copyViolations: CopyViolation[];
  baselineIssues: ContractIssue[];
  errorIssues: ContractIssue[];
  parityIssues: ContractIssue[];
  counts: Record<string, number>;
  errorCodes: string[];
}

export declare function tokenize(src: string): {
  masked: string;
  strings: { value: string; start: number; line: number; quote: string }[];
};
export declare function scanSource(relPath: string, src: string): CopyViolation[];
export declare function scanUserCopy(root?: string): CopyViolation[];
export declare function countByFile(problems: CopyViolation[]): Record<string, number>;
export declare function loadBaseline(path?: string): BaselineFile;
export declare function checkBaseline(
  problems: CopyViolation[],
  baseline?: BaselineFile
): ContractIssue[];
export declare function goErrorCodes(goSource?: string): string[];
export declare function checkErrorMessages(codes?: string[]): ContractIssue[];
export declare function checkLocaleParity(): ContractIssue[];
export declare function checkAll(): ContractResult;
