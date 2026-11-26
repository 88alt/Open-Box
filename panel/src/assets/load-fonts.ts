export const loadFonts = () => {
  if (import.meta.env.MODE === 'cdn-fonts') {
    const createLink = (href: string) => {
      const link = document.createElement('link')
      link.rel = 'stylesheet'
      link.href = href
      link.media = 'print'
      link.onload = () => {
        link.media = 'all'
      }
      document.head.appendChild(link)
    }

    createLink('https://unpkg.com/subsetted-fonts@latest/MiSans-VF/MiSans-VF.css')
    createLink('https://unpkg.com/subsetted-fonts@latest/SarasaUiSC-Regular/SarasaUiSC-Regular.css')
    createLink('https://unpkg.com/subsetted-fonts@latest/PingFangSC-Regular/PingFangSC-Regular.css')
    createLink('https://unpkg.com/@fontsource/fira-sans')
  } else if (import.meta.env.MODE === 'MiSans') {
    import('subsetted-fonts/MiSans-VF/MiSans-VF.css')
  } else if (import.meta.env.MODE === 'SarasaUi') {
    import('subsetted-fonts/SarasaUiSC-Regular/SarasaUiSC-Regular.css')
  } else if (import.meta.env.MODE === 'PingFang') {
    import('subsetted-fonts/PingFangSC-Regular/PingFangSC-Regular.css')
  } else if (import.meta.env.MODE === 'FiraSans') {
    import('@fontsource/fira-sans/index.css')
  } else if (import.meta.env.MODE === 'SystemUI') {
  } else {
    // 默认构建不加载任何自带字体,与上游 v0.1.488 的发布包一致:上游把面板字体固定成 SystemUI、
    // 设置里没有字体选项,包里也没有这些字体资源(上游 assets 5.6 MB,main 带字体时 15 MB)。
    // 要 MiSans / Sarasa / PingFang / Fira 的构建用 build:misans-only 这类显式模式。
  }
}
