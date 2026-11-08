// Vite 的 `?raw` 导入（把文件内容当字符串读进来）。
//
// 只为了给 `import indexHtml from "../../index.html?raw"` 这类写法一个类型：
// 前端没有 @types/node，用 node:fs 读仓库文件会让 typecheck 直接失败，
// 而品牌守卫（BRAND）恰恰需要读 index.html 这种不经过 React 的文件。
declare module "*?raw" {
  const content: string;
  export default content;
}
