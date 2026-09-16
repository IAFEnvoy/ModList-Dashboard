package badge

// 本文件由 scripts/sync-devins-badges.ps1 生成，请勿手改。
//
// 图标形状与品牌配色取自 @intergrav/devins-badges (CC0-1.0) 的 compact 成品矢量。
// 上游把文字烘焙成了路径，无法只改排版，因此这里只复用图形，
// 文字改由本项目模板渲染（这样才能两行显示、也能本地化）。
//
// 图标在上游 40px 设计与本项目 56px 画布中的像素尺寸一致，
// 因此路径坐标原样保留，仅用 transform 平移进 40x40 图标区。
//
// 内嵌位图的图标（NeoForge 是一张 128x128 PNG）由 Convert-PatternImageToImage
// 改写成直接拉伸的 <image>：上游那种「pattern 填矩形」写法在
// patternContentUnits=objectBoundingBox 下只会画出约 1px，实测会被裁成一个灰点。

// badgeIconFabric 来自 fabric_vector.svg
const badgeIconFabric = "<g clip-path=\"url(#badge_icon_clip)\"><g transform=\"translate(12 8)\">" +
	"<path fill=\"#38342a\" d=\"M22.971 6v2h-1.938v4h-1.939v2h-1.938v2h-1.939v2h-1.939v2H11.34v2H9.4v4.001h1.939v2h1.938v2h1.939v2h1.939v2h3.877v-2h1.938v-4h1.939v-2h1.938v-2h1.939v-2h3.877v-2h1.939v-4h-1.939v-2h-1.939v-2h-1.938v-2h-1.939V8H24.91V6z\"/>" +
	"<path fill=\"#dbd0b4\" d=\"M22.972 8v2h1.938v2h1.939v2h1.938v2h1.939v-2h-1.939v-2H26.85v-2h-1.94V8z\"/>" +
	"<path fill=\"#c6bca5\" d=\"M22.972 10v2h1.938v-2z\"/>" +
	"<path fill=\"#dbd0b4\" d=\"M21.033 12v4h-3.878v2h-1.938v2h-1.939v4h1.939v2.001h1.938v2h3.878v-4h-1.939v-2h3.877v2h1.939v-4h-1.94v-2h3.877v-2H24.91v-2h-1.94v-2z\"/>" +
	"<path fill=\"#38342a\" d=\"M22.972 12v2h1.938v2h1.939v2h1.938v-2H26.85v-2h-1.94v-2z\"/>" +
	"<path fill=\"#bcb29c\" d=\"M24.91 12v2h1.938v-2z\"/><path fill=\"#c6bca5\" d=\"M19.094 14v2h1.938v-2z\"/>" +
	"<path fill=\"#bcb29c\" d=\"M26.848 14v2h1.939v-2z\"/><path fill=\"#c6bca5\" d=\"M21.032 16v2h1.939v-2z\"/>" +
	"<path fill=\"#bcb29c\" d=\"M28.787 16v2h1.939v-2z\"/><path fill=\"#c6bca5\" d=\"M22.972 18v2h3.877v-2z\"/>" +
	"<path fill=\"#bcb29c\" d=\"M26.848 18v2H24.91v4h1.938v-2h1.939v-4z\"/>" +
	"<path fill=\"#807a6d\" d=\"M30.726 18v2h1.938v-2z\"/><path fill=\"#c6bca5\" d=\"M17.156 20v2h1.938v-2z\"/>" +
	"<path fill=\"#aea694\" d=\"M11.34 22v2h1.938v2.001h1.939v2h1.938v2h1.94v-2h-1.94v-2h-1.938v-2h-1.939v-2z\"/>" +
	"<path fill=\"#c6bca5\" d=\"M19.094 22v2h3.877v-2z\"/><path fill=\"#9a927e\" d=\"M11.34 24v2.001h1.938v-2z\"/>" +
	"<path fill=\"#bcb29c\" d=\"M21.032 24v4.001h1.939v-2h1.938v-2z\"/>" +
	"<path fill=\"#9a927e\" d=\"M13.278 26.001v2h1.939v-2zm1.939 2v2h1.938v-2z\"/>" +
	"<path fill=\"#bcb29c\" d=\"M19.094 28.001v2h1.938v-2z\"/><path fill=\"#9a927e\" d=\"M17.156 30.001v2h1.938v-2z\"/></g>" +
	"</g>"

// badgeIconForge 来自 forge_vector.svg
const badgeIconForge = "<g clip-path=\"url(#badge_icon_clip)\"><g transform=\"translate(12 8)\">" +
	"<path fill=\"#dfa86a\" d=\"M33.329 14.766 21.3 14.167H36V13H19.39v4.415c0 .032-.477-2.87-.604-3.69h-1.305v4.1c0 .031-.572-3.437-.604-3.879H8c.605.505 3.946 3.343 6.332 4.51 1.177.567 2.641.599 3.946.63.668.032 1.336.063 1.845.568.732.694.891 1.797.255 2.617-.605.82-2.323 1.01-2.323 1.01l-1.464 1.702V27h3.278l.095-1.986 2.832-1.987c-.286.252-.986.883-1.973 2.428a5 5 0 0 0-.54 1.104c.7-.6 2.163-1.01 3.881-1.01 1.687 0 3.15.41 3.85 1.01a5 5 0 0 0-.54-1.104c-1.018-1.545-1.687-2.176-1.973-2.428l2.832 1.987.095 1.986h3.055v-2.018l-1.432-1.734s-2.132-.127-2.673-1.01c-1.654-2.459.605-6.306 5.95-7.473\"/>" +
	"</g></g>"

// badgeIconNeoForge 来自 neoforge_vector.svg
const badgeIconNeoForge = "<g clip-path=\"url(#badge_icon_clip)\"><g transform=\"translate(12 8)\">" +
	"<image x=\"7.993\" y=\"6.497\" width=\"28\" height=\"28\" preserveAspectRatio=\"none\" xlink:href=\"data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAIAAAACACAYAAADDPmHLAAAEEElEQVR4nOydP0iUcRjHT9MQByvtz5Uk6GBESyAtCRU0FDblEkpDBUlQQkQu9u+IaDFaLIiiWiKIpsYWKdEGw6l/JJQpnF7WWS0iSRq1RTzPa7+717v4fj7rl/d3z919eOC55+690gRIgwDiIIA4ZV7Yc/XawtKVAnHRdepEiZXRAcRBAHEQQBwEEAcBxEEAccq8Ua+ldV/Qoauqq3OpKe98mZ4udAmFxnyP6QDiIIA4CCAOAoiDAOIggDjuNtAb5wb6+s2sZt1qM6usrFxsbX9x9+LZoOsOn79kZjMzM2b24MrloMc7cLo76Loosh8/m5n3mnvQAcRBAHEQQBwEEAcBxEEAcdwx8M2r12a2sb7OzLzRyssSEaPXprpaM3s7nnbPtQgdS71avOeQy4gYOup50AHEQQBxEEAcBBAHAcRBAHHcMTC5PmlmmcmMmXmjVdRGzxuv2o8dN7ML3fZ49e5ch/uYJuVrg2q5f+O6mUVtGEPHRG9T6EEHEAcBxEEAcRBAHAQQBwHEQQBx3M8BvFnfw1v57pibcq/tH7ezD0MTQefW1vxwH9M8M2uf6dXiraajnn8o3qp47P2YmdEBxEEAcRBAHAQQBwHEQQBx3DGwELRVTZrZbN8ZM2tqnDOzFcmKoFqSmVkzyzi1tFXZZ6azy4JqiYJ1MASBAOIggDgIIA4CiIMA4iz5GBi1mUvW2ONcMRFXnfOxnGpDBxAHAcRBAHEQQBwEEAcBxIllDCy92WlmUeNTQ2rQzKbTo2b29dbBRVb3J9+cjZ/HyqP3zKy6tt6+MNXsnptxXrv5jt7FFfcP0AHEQQBxEEAcBBAHAcRBAHHKpibDvkzo4Y0r3pjziwYn27n/kJk9arGvCx31PLxaXgw9NbNMttw9t+Lk7aB6sk7mvcd0AHEQQBwEEAcBxEEAcRBAnOBtoPdbtO937D9q9H7Dl4jY+GU/2b8bTCTWmEnU6GXhbS69WrznELUNHXZua7v8SNgfZ3rQAcRBAHEQQBwEEAcBxEEAcUq6ulILVri3dY95YegtSbwRMfF7TMz/5q6YGB7xb1cTx6j3fHDYzOgA4iCAOAggDgKIgwDiIIA4RXen0NDN3fbeJ2a2ob4x6MyJ0REze9a5K+jMYoMOIA4CiIMA4iCAOAggDgKIgwDiLPnnAFHrznTEujiElwOP835m6D9/xLHuzQU6gDgIIA4CiIMA4iCAOAggjjsGet/89b5puq25Kbig0DHpYfvu4McMoRDjXByvOR1AHAQQBwHEQQBxEEAcBBAneBu4Zevm/FaSI8W2ZYuDOF5zOoA4CCAOAoiDAOIggDgIIE6JF3r3D4L/h56elPk+0wHEQQBxEEAcBBAHAcRBAABl6ADiIIA4CCDOzwAAAP//EwrbMM5FtI4AAAAASUVORK5CYII=\"/>" +
	"</g></g>"

// badgeIconDocs 来自 ghpages_vector.svg
const badgeIconDocs = "<g clip-path=\"url(#badge_icon_clip)\"><g transform=\"translate(12 8)\">" +
	"<path fill=\"#e8e8e8\" fill-rule=\"evenodd\" d=\"M9 10.223A1.224 1.224 0 0 1 10.219 9h6.91c1.995 0 3.766.962 4.876 2.447A6.09 6.09 0 0 1 26.885 9h6.898A1.217 1.217 0 0 1 35 10.223v17.112a1.224 1.224 0 0 1-1.218 1.223h-7.324a3.65 3.65 0 0 0-2.586 1.074l-1.01 1.012a1.217 1.217 0 0 1-1.723 0l-1.01-1.012a3.66 3.66 0 0 0-2.586-1.074H10.22A1.217 1.217 0 0 1 9 27.335zm14.227 4.889c0-.973.385-1.905 1.07-2.593a3.65 3.65 0 0 1 2.586-1.074h5.68v14.668h-6.106a6.1 6.1 0 0 0-3.237.932zm-2.444 11.937.006-8.268-.003-3.672c0-.972-.387-1.904-1.072-2.591a3.65 3.65 0 0 0-2.584-1.073h-5.692v14.668h6.105a6.1 6.1 0 0 1 3.24.936\" clip-rule=\"evenodd\"/>" +
	"</g></g>"

// badgeAssetStyle 上游资源的品牌配色：强调色与背景渐变。
var badgeAssetStyle = map[string]struct{ Accent, BgStart, BgEnd string }{
	"badgeIconFabric":   {Accent: "#bcb29c", BgStart: "#2d2a24", BgEnd: "#1e1c18"},
	"badgeIconForge":    {Accent: "#dfa86a", BgStart: "#251e17", BgEnd: "#1b1610"},
	"badgeIconNeoForge": {Accent: "#d7742f", BgStart: "#251d17", BgEnd: "#1b1510"},
	"badgeIconDocs":     {Accent: "#FFFFFF", BgStart: "#1b2330", BgEnd: "#10151d"},
}
