# new-api 对标 SnowAPI UI 深度重构开发设计规范与实施方案

> **文档性质**：前端工程架构与系统级设计规范（Architecture & Implementation Specification）  
> **更新时间**：2026-10-06  
> **状态**：规范草案 / 指导实施（Canonical Design Document）  
> **对标参考仓**：`Ooxygen7/SnowAPI` (`/private/tmp/snowapi-ref`)  
> **目标工程**：`dengyie/new-api` (`/Users/mango/newapi-test`)

---

## 1. 深度逆向诊断：差距的本质是什么？

在初期的快速改造中，我们通过增加暖纸色背景、将控制台主色调锁为墨黑单色、以及给部分按钮补充药丸形状（`border-radius: 999px`），在表面色阶上实现了初步对齐。但在整体视觉感受与交互质感上，依然无法达到上游参考项目 `SnowAPI` 的水准。

通过对 `/private/tmp/snowapi-ref` 源码的逐层逆向分析，我们解构出 5 个维度的系统性差距：

| 维度 | 当前项目状态 | SnowAPI 完整精髓 | 差距根因 |
|---|---|---|---|
| **控制台布局骨架** | 两段式：独立顶栏 `AppHeader` (带全宽分割线) + 侧边栏 `AppSidebar` + 下层 `SectionPageLayout` | **统一外壳**：`<AstryxAppShell>` 消灭独立顶栏，侧栏贯通全屏，抽屉式移动导航，内容区顶格自适应 | 缺乏无顶栏的现代 SaaS 布局基底，视觉结构仍然停留在传统管理系统 |
| **首屏渲染与数据流** | 各页面子组件内部分散加载，或者依赖传统的旋转 loading，内容到位后直接硬跳 | **双层渐进加载**：利用 TanStack Query 的 `useIsFetching` 驱动全屏淡入骨架遮罩，配合 `<AnimatedOutlet>` 页面级转场 | 缺少统一的数据获取状态感知层，数据到达时的帧率与视觉跳动破坏质感 |
| **落地页动效体系** | 打字机时间线终端 + 纯 CSS 横纹字标，缺乏物理层叠质感与微动效 | **Poolside 动效矩阵**：`gsap` + `motion` 驱动的 `CardSwap`（3D 悬浮层叠卡片）、`ShuffleText`（字符解码打乱）、`ThreadsBackground`（流体线条 WebGL） | 核心动态资产缺失，使得落地页缺少科技与艺术融合的冲击力 |
| **排版与字体系统** | 回退到系统默认无衬线字体，字阶层次单一 | **双轴可变字体系统**：`Public Sans Variable`（极简工业冷峻体）+ `Lora Variable`（社论级衬线体）在 CSS Layer 顶层声明 | 缺少具有高辨识度与现代质感的可变排版字体层 |
| **特化与专属视图** | 登录页仅为简单表单居中；模型列表为通用卡片/表格 | **Bespoke 质感单页**：`/sign-in` 采用纯黑 `#000` 画布搭配 WebGL `metallic-paint` 液态金属着色器；`/model-list` 包含专属 `model-health-bar` 实时四档健康度与资助标签 | 关键场景缺乏定制级的高端视觉交互 |

---

## 2. 核心架构拓扑与依赖规范

### 2.1 依赖体系（Dependencies）

SnowAPI 的核心质感依托于一套成熟的高端 UI 基础设施与动效库：

```json
{
  "dependencies": {
    "@astryxdesign/core": "0.2.0",
    "@astryxdesign/theme-neutral": "0.2.0",
    "@fontsource-variable/public-sans": "^5.2.7",
    "@fontsource-variable/lora": "^5.2.8",
    "motion": "^12.42.2",
    "gsap": "^3.13.0",
    "ogl": "^1.0.11",
    "@hugeicons/react": "^1.1.9",
    "@hugeicons/core-free-icons": "^4.2.2"
  }
}
```

- `@astryxdesign/core`: 提供 SaaS AppShell 布局、折叠侧边栏、响应式抽屉、国际化与路由 Link Provider。
- `motion` & `gsap`: 负责平滑补间动画、贝塞尔曲线缓动（`cubic-bezier(0.22, 1, 0.36, 1)`）与 3D 变换。
- `ogl`: 轻量级 WebGL 渲染管线，驱动液态金属（Metallic Paint）着色器与流体线条（Threads）。
- 可变字体包：提供无级字重（100~900）与光学尺寸自适应，体积小且免去多阶段字体闪烁（FOUT）。

### 2.2 CSS Layer 体系结构

在 Tailwind CSS 4 (`@import 'tailwindcss'`) 架构下，全局样式的分层必须严格遵循优先级，避免类名冲突与权重覆盖灾难：

```css
@layer reset, theme, base, astryx-base, astryx-theme, components, utilities;

@import '@astryxdesign/core/astryx.css';
@import './site-design.css';
@import './astryx-app-shell.css';
@import './console-skin.css';
@import './public-shell.css';
```

---

## 3. 控制台外壳重塑方案：Astryx AppShell

### 3.1 废除独立顶栏，统一全屏外壳

传统 new-api 结构：
`Viewport -> AppHeader (56px 顶栏，含用户信息、主题切换、移动端汉堡包) -> Body (AppSidebar + PageContent)`

SnowAPI Astryx 结构：
`Viewport -> <AppShell> (variant="surface", height="fill")`
- **侧边栏 (SideNav)**：
  - 顶栏 Brand Row：内嵌 Logo、站名与折叠触发器 `<SideNavCollapseButton>`；
  - 菜单分组：`<SideNavSection>` 包裹 `<SideNavItem>`，支持折叠与徽标 `<NavigationBadge>`；
  - 底部 Footer：集成 `<SidebarSignOutButton>` 与订阅/配额卡片。
- **移动端适配 (SnowMobileNavigation)**：
  - 断点：`breakpoint="md"`；
  - 采用无遮挡右侧滑出抽屉 `<SheetContent>`，自带平滑阻尼转场；
  - 点击任何 `<a>` 路由链接自动收起抽屉。
- **内容容器 (Content Frame)**：
  - 声明 `@container/content` 容器查询；
  - 内容区直接占满剩余视口（`flex-1 min-h-0 overflow-y-auto`）。

### 3.2 数据感知与转场骨架

在 `AstryxAppShell` 内部挂载数据感知与页面过渡：

```tsx
const initialQueryFetches = useIsFetching({
  predicate: (query) =>
    query.state.fetchStatus === 'fetching' && query.state.data === undefined,
})

<div className="snowapi-astryx-content @container/content">
  <div
    className="snowapi-console-content-state"
    data-loading={initialQueryFetches > 0 || undefined}
  >
    {initialQueryFetches > 0 ? (
      <ContentLoading className="snowapi-console-loading-indicator absolute inset-0 z-10 min-h-0" />
    ) : null}
    <div className="snowapi-console-loaded-content flex min-h-0 flex-1 flex-col">
      <AnimatedOutlet />
    </div>
  </div>
</div>
```

此机制保证了在后台路由切换或初始加载时，整个内容区呈现出优雅的半透明骨架屏，而非生硬的 DOM 闪烁。

---

## 4. 营销落地页：Poolside 动效与组件规范

### 4.1 核心动效组件解剖

1. **CardSwap (3D 层叠悬浮翻转)**:
   - 位置：`features/home/components/card-swap.tsx`
   - 机制：利用 `motion/react` 的 3D 透视（`perspective: 1200px`），维护一个 3 张卡片的循环堆叠栈。每隔固定周期，顶层卡片沿 Y 轴与 Z 轴向后翻转平移，底层卡片前推，伴随平滑景深模糊。
2. **ShuffleText (字符解码解码打乱)**:
   - 位置：`features/home/components/shuffle-text.tsx`
   - 机制：基于预设字形字符集（英文大写/数字/特殊字符），在文字进入视口时执行逐字随机打乱动画，由左向右依次锁定正确字符，呈现极客数字解码质感。
3. **ThreadsBackground (WebGL 纤维流体线条)**:
   - 位置：`features/home/components/threads-background.tsx`
   - 机制：使用轻量级 `ogl` 编写基于顶点与片元着色器的流体曲线，通过鼠标指针交互或自动缓动正弦波，生成平滑流动的背景网格线条。
4. **StripedWordmark (条纹自适应字标)**:
   - 机制：恒定 viewBox `1120×210`，基于字符数动态缩放字号并严密建模 `-0.07em` tracking；包含 36 条斜向扫描横纹，在视口内触发时执行单次扫光，非视口下保持沉静斜纹。

### 4.2 动效性能与可访问性铁律

- **帧率钳制与后台冻结**：基于 `use-home-motion.ts`，监听 `document.visibilitychange` 与 `IntersectionObserver`。页面不可见或离开视口时，立刻暂停 `requestAnimationFrame` 循环；时间步长使用 `Math.min(now - prev, 100)` 钳制，杜绝后台切回时的动画瞬跳。
- **减弱动态偏好（Reduced Motion）**：遇到 `@media (prefers-reduced-motion: reduce)`，所有 GSAP/Motion 补间时长置为 `0.01ms`，WebGL 降级为静态 SVG 纹理或渐变背景。

---

## 5. 专属特化单页（Bespoke Pages）

### 5.1 纯黑金属着色器登录页 (`/sign-in`)

- **画布环境**：全黑 `#000000` 背景，彻底独立于暖纸色体系。
- **Metallic Paint 渲染**：
  - 基于 WebGL 2.0 着色器管线；
  - 顶点着色器投影四边形；片元着色器计算液态金属法线、高光反射与折射波纹；
  - 用户指针移动时引发微弱的流体流动响应；
  - 在移动端或不支持 WebGL 的环境下平滑回退至 CSS 径向渐变。

### 5.2 高密度模型列表页 (`/models` / `/model-list`)

- **Model Health Bar**：
  - 动态四档健康度指标（99%+ 翠绿、95-99% 浅绿、90-95% 琥珀黄、<90% 玫瑰红）；
  - 实时响应毫秒级延迟与成功率波动。
- **Model Funding Badge**：
  - 针对免费/赞助/开源/自建模型挂载微型胶囊徽章。

---

## 6. 全局设计令牌与样式覆写矩阵

### 6.1 暖纸色与水墨黑令牌 (`site-design.css`)

```css
html[data-site-design='paper'] {
  --color-paper: #fbfaf6;
  --color-ink: #11110f;
  --color-surface: #f7f6ef;
  --color-inset: #efeee7;
  --color-muted: #6f6d69;
  --color-rule: #e4e1d7;
  --color-emphasis: #d4d0c4;
}

html[data-site-design='paper'].dark {
  --color-paper: #141413;
  --color-ink: #f5f4ef;
  --color-surface: #1e1e1a;
  --color-inset: #292822;
  --color-muted: #aaa79e;
  --color-rule: #37362f;
  --color-emphasis: #4a483e;
}
```

### 6.2 控制台覆写层 (`console-skin.css`) 核心准则

- **Card**：彻底杀掉任何 `box-shadow` 与实体 `border`，采用 `--ui-surface` 底色结合 `color-mix` 边框；
- **Button**：Default 变体统一收敛为药丸（`border-radius: 999px`），背景锁定纯前景色 `--foreground`，文字反白；
- **Input**：外框 1px 实线，聚焦状态采用 1px 实心环 `0 0 0 1px var(--foreground)`，杜绝半透明 3px 发光环；
- **Table**：表头小写 `0.6875rem`，禁用强制大写，单元格采用等宽数字排版，自带平滑原生滚动槽。

---

## 7. 分阶段演进与实施路线图（SOP）

为确保生产环境安全与代码库稳定性，重构应分阶段推进，每阶段独立通过全量门禁：

```
Phase 1: 全局设计层与样式桥接 (已完成验收)
  ├── 落地 site-design.css (暖纸色 token 桥接 45 个变量)
  ├── 落地 console-skin.css (药丸按钮/三级表面/聚焦环/单色锁定)
  └── 补齐 button 属性透传与自动化测试

Phase 2: 架构级外壳改造 (Astryx AppShell 引入)
  ├── 引入 @astryxdesign/core 与主题包
  ├── 重写 components/layout/components/astryx-app-shell.tsx
  ├── 移除旧版 AppHeader 与 AppSidebar 的两段式结构
  ├── 接入 TanStack Query useIsFetching 骨架屏转场
  └── 验证桌面端折叠与移动端抽屉 Sheet

Phase 3: 首页 Poolside 动效与组件矩阵
  ├── 引入 gsap, motion, ogl
  ├── 移植 CardSwap (3D 翻转卡片)、ShuffleText (字符解码)
  ├── 接入 ThreadsBackground (流体 WebGL)
  └── 对齐首页完整栅格排版与断点规范

Phase 4: 特化单页与视觉精细化打磨
  ├── 落地 /sign-in 纯黑金属着色器登录页
  ├── 落地 /models 实时健康度进度条与特化视图
  └── 建立多终端视口自动化验收回归测试
```

---

## 8. 质量门禁与生产发布规范

每次交付必须严格通过以下流水线门禁：
1. **类型检查**：`bun run typecheck` (tsgo -b，0 错误)；
2. **代码风格与规范**：`bun run lint` (oxlint) 与 `bun run format:check` (oxfmt) 100% 通过；
3. **单元与集成测试**：`bun test` (vitest 全量测试通过，覆盖率不降低)；
4. **未用导出检查**：`bun run knip` 保证死代码不膨胀；
5. **生产打包构建**：`bun run build` 产出完整静态资源，断言 `web/dist/index.html` > 1000 字节，杜绝白屏二进制事故；
6. **实机浏览器验证**：在 1440px / 1024px / 768px / 390px 四档视口下完成登录态与公开态的功能及渲染核验。

---

## 9. 核心架构缺陷与根因修复记录 (2026-10-06)

在审查与集成验收中，针对控制台外壳、导航路由以及组件生命周期进行了深度根因级修复与死代码收敛：

1. **LinkProvider 路由多态协议阻抗匹配 (`TanStackLinkAdapter`)**
   - **根因分析**：`@astryxdesign/core` 的 `LinkProvider` 默认期望向子组件传递 Next.js 风格的 `href` 属性，而 `@tanstack/react-router` 的 `<Link>` 组件核心属性为 `to`。若直接注入 `<LinkProvider component={Link}>`，未带 `to` 的侧边栏导航链接会退化为根路由 `<a href="/">`，导致控制台所有菜单项点击跳转失效。
   - **修复方案**：在 `astryx-app-shell.tsx` 中编写 `TanStackLinkAdapter`，通过 `forwardRef` 代理并将 `to: (to ?? href)` 自动转换映射，确保侧边栏所有内部链接均被 TanStack Router 正常拦截接管。

2. **嵌套子级视图返回项图标补齐**
   - **根因分析**：`AstryxNavigation` 在渲染下钻视图（如 `/system-settings/*`）时，`topContent` 中的返回按钮仅指定了 `label`，未传入 `icon`。在侧边栏折叠为 `3rem` 的窄态下，Astryx 会隐藏文本标签仅保留图标；缺少图标会导致该返回入口被无声过滤或出现空块。
   - **修复方案**：在 `astryx-navigation.tsx` 中引入 `lucide-react` 的 `ArrowLeft` 图标，赋予子视图返回按钮明确的视觉锚点，保证折叠与展开态体验一致。

3. **异常处理路由遗留依赖解耦 (`_authenticated/errors/$error.tsx`)**
   - **根因分析**：旧版 `$error.tsx` 页面直接内联了旧版 `<Header>`、`<SidebarTrigger>` 与 `<ConfigDrawer>`。在外层布局迁移至 `AstryxAppShell` 并移除旧版 `SidebarProvider` / `LayoutProvider` 后，访问任何错误路由均会导致 React 抛出缺失 Provider 上下文的致命异常并白屏。
   - **修复方案**：彻底移除 `$error.tsx` 内嵌的旧版顶栏与抽屉，直接由外层 `AstryxAppShell` 承载页面骨架，子级仅渲染纯净的语义化错误视图。

4. **动效可见性 Hook DOM 挂载时序容错 (`useVisibleMotion`)**
   - **根因分析**：原实现使用静态 `useRef<T>(null)` 并在初次挂载时执行 `useEffect([], ...)`。当目标节点处于条件渲染、懒加载或动画过渡容器中时，初次渲染 `ref.current` 为 `null`，导致 `IntersectionObserver` 永远不会绑定目标元素，动画处于永久假死。
   - **修复方案**：重构为兼具函数回调能力与 `.current` 读取特性的 `StatefulRef`（Callback Ref），挂载触发时即时更新受控状态并启动 `IntersectionObserver`，彻底杜绝挂载时序竞争问题。

5. **全站废弃死代码清理与导出收敛**
   - 彻底删除 9 个无调用死代码文件：`logo.tsx`、`app-header.tsx`、`app-sidebar.tsx`、`nav-group.tsx`、`sidebar-view-header.tsx`、`chat-presets-item.tsx`、`header.tsx`、`layout-provider.tsx`、`config-drawer.tsx`。
   - 清理 `components/layout/index.ts` 废弃导出，保持公开模块接口紧凑整洁。

