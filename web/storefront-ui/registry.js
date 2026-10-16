// Stored IDs are a public compatibility contract. Never rename them in a database.
export const themes = Object.freeze([
  {id:'minimal', design:'minimal', name:'极简旗舰', description:'真白与纯黑，笔记本主视觉与轻盈商品陈列'},
  {id:'dark-business', design:'orbital', name:'暗黑科技', description:'地球弧面与冰蓝辉光，以搜索连接数字工具'},
  {id:'card-catalog', design:'warm', name:'生活方式', description:'暖阳工作桌、手写标题与陶土色细节'},
  {id:'compact-list', design:'arcade', name:'游戏像素', description:'原创像素世界、游戏招牌与道具式商品栏'},
  {id:'editorial', design:'editorial', name:'杂志极简', description:'非对称编辑排版，雪山工作空间与清晰留白'},
  {id:'playful', design:'neon', name:'赛博霓虹', description:'原创动漫人物、紫青城市与细霓虹边线'},
].map(Object.freeze));
export function themeFor(value) {
  return themes.find(t => t.id === value || t.design === value) || themes[0];
}
