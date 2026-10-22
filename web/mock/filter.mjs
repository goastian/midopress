// Mini parser and evaluator of the rules language (a subset of the mitmproxy
// filter syntax): ~d ~u ~t ~c ~m ~h ~hq ~hs ~b ~q ~s ~src ~dst ~all ~none,
// the operators & | ! and parentheses. Arguments can be quoted or not.

export class FilterError extends Error {
  constructor(message, column) {
    super(message)
    this.column = column
  }
}

export const HOST_ONLY = new Set(['d', 'src', 'dst'])
const NO_ARG = new Set(['all', 'none', 's', 'q'])
const REGEX_ATOMS = new Set(['d', 'u', 't', 'm', 'h', 'hq', 'hs', 'b'])
const KNOWN = new Set([...NO_ARG, ...REGEX_ATOMS, 'c', 'src', 'dst'])

function compileAddr(arg, argCol) {
  const m = /^(\d{1,3}(?:\.\d{1,3}){3})(?:\/(\d{1,2}))?$/.exec(arg)
  if (!m) {
    if (/^[0-9a-fA-F:.]+$/.test(arg) && arg.includes(':')) return (a) => a === arg
    throw new FilterError(`"${arg}" is not an IP address or a CIDR`, argCol)
  }
  const bits = m[2] === undefined ? 32 : Number(m[2])
  if (bits > 32) throw new FilterError(`invalid prefix length /${bits}`, argCol)
  const toInt = (s) => s.split('.').reduce((acc, p) => acc * 256 + Number(p), 0)
  const base = toInt(m[1])
  const size = 2 ** (32 - bits)
  return (addr) => {
    if (!/^\d{1,3}(\.\d{1,3}){3}$/.test(addr || '')) return false
    const v = toInt(addr)
    return Math.floor(v / size) === Math.floor(base / size)
  }
}

/** Parses an expression. Throws FilterError with a 1-based column. */
export function parseFilter(src) {
  let i = 0
  const ws = () => {
    while (i < src.length && /\s/.test(src[i])) i++
  }

  function parseOr() {
    let left = parseAnd()
    ws()
    while (src[i] === '|') {
      i++
      left = { type: 'or', l: left, r: parseAnd() }
      ws()
    }
    return left
  }

  function parseAnd() {
    let left = parseUnary()
    ws()
    while (src[i] === '&') {
      i++
      left = { type: 'and', l: left, r: parseUnary() }
      ws()
    }
    return left
  }

  function parseUnary() {
    ws()
    if (i >= src.length) throw new FilterError('unexpected end of expression', src.length + 1)
    const c = src[i]
    if (c === '!') {
      i++
      return { type: 'not', x: parseUnary() }
    }
    if (c === '(') {
      const open = i
      i++
      const x = parseOr()
      ws()
      if (src[i] !== ')') {
        throw new FilterError(`missing ")" for the "(" at column ${open + 1}`, Math.min(i, src.length) + 1)
      }
      i++
      return x
    }
    if (c === '~') return parseAtom()
    throw new FilterError(`unexpected "${c}", expected a filter such as ~d or ~t`, i + 1)
  }

  function readArg(name, atomCol) {
    ws()
    if (i >= src.length || '&|)'.includes(src[i])) {
      throw new FilterError(`~${name} needs an argument`, atomCol)
    }
    const argCol = i + 1
    const q = src[i]
    if (q === '"' || q === "'") {
      i++
      let out = ''
      while (i < src.length && src[i] !== q) {
        if (src[i] === '\\' && src[i + 1] === q) {
          out += q
          i += 2
        } else out += src[i++]
      }
      if (i >= src.length) throw new FilterError('unterminated quoted argument', argCol)
      i++
      return { arg: out, argCol }
    }
    const start = i
    let depth = 0
    while (i < src.length && !/\s/.test(src[i])) {
      if (src[i] === '(') depth++
      else if (src[i] === ')') {
        if (depth === 0) break
        depth--
      }
      i++
    }
    return { arg: src.slice(start, i), argCol }
  }

  function parseAtom() {
    const col = i + 1
    const m = /^~([a-z]+)/.exec(src.slice(i))
    if (!m || !KNOWN.has(m[1])) {
      throw new FilterError(`unknown filter "${m ? m[0] : '~'}"`, col)
    }
    const name = m[1]
    i += m[0].length
    const node = { type: 'atom', name, col, arg: '' }
    if (NO_ARG.has(name)) return node
    const { arg, argCol } = readArg(name, col)
    node.arg = arg
    node.argCol = argCol
    if (REGEX_ATOMS.has(name)) {
      try {
        node.re = new RegExp(arg, 'i')
      } catch (e) {
        throw new FilterError(`invalid regular expression: ${e.message.replace(/^Invalid regular expression: /, '')}`, argCol)
      }
    } else if (name === 'c') {
      if (!/^\d{3}$/.test(arg)) throw new FilterError(`~c needs a three digit status code, not "${arg}"`, argCol)
      node.code = Number(arg)
    } else {
      node.addr = compileAddr(arg, argCol)
    }
    return node
  }

  const ast = parseOr()
  ws()
  if (i < src.length) throw new FilterError(`unexpected "${src[i]}"`, i + 1)
  return ast
}

export function atomsOf(node, out = []) {
  if (node.type === 'atom') out.push(node)
  else if (node.type === 'not') atomsOf(node.x, out)
  else {
    atomsOf(node.l, out)
    atomsOf(node.r, out)
  }
  return out
}

const headerLines = (h) =>
  Object.entries(h || {}).flatMap(([k, v]) => (Array.isArray(v) ? v : [v]).map((x) => `${k}: ${x}`))

function atomResult(n, ctx) {
  const tag = `~${n.name}${n.arg ? ' ' + n.arg : ''}`
  const yes = (what) => ({ value: true, reason: `${tag}: ${what}` })
  const no = (what) => ({ value: false, reason: `${tag}: ${what}` })
  const test = (value, text, label) =>
    n.re.test(value || '') ? yes(`${label} "${text ?? value}" matches`) : no(`${label} "${text ?? value}" does not match`)
  switch (n.name) {
    case 'all':
      return yes('always')
    case 'none':
      return no('never')
    case 's':
      return ctx.hasResponse ? yes('there is a response') : no('no response yet')
    case 'q':
      return !ctx.hasResponse ? yes('request only') : no('there is a response')
    case 'd':
      return test(ctx.host, ctx.host, 'host')
    case 'u':
      return test(ctx.url, ctx.url, 'url')
    case 'm':
      return test(ctx.method, ctx.method, 'method')
    case 't':
      if (!ctx.hasResponse || !ctx.contentType) return no('needs the response content type, none given')
      return test(ctx.contentType, ctx.contentType, 'content type')
    case 'c':
      if (!ctx.hasResponse || !ctx.status) return no('needs the response status, none given')
      return ctx.status === n.code ? yes(`status is ${ctx.status}`) : no(`status is ${ctx.status}`)
    case 'h':
    case 'hq':
    case 'hs': {
      const lines = [
        ...(n.name !== 'hs' ? headerLines(ctx.reqHeaders) : []),
        ...(n.name !== 'hq' && ctx.hasResponse ? headerLines(ctx.resHeaders) : []),
      ]
      return lines.some((l) => n.re.test(l)) ? yes('a header matches') : no('no header matches')
    }
    case 'b':
      return n.re.test(ctx.body || '') ? yes('the body matches') : no('the body does not match')
    case 'src':
      return n.addr(ctx.src) ? yes(`${ctx.src} is inside`) : no(`${ctx.src || 'unknown address'} is outside`)
    case 'dst':
      return n.addr(ctx.dst) ? yes(`${ctx.dst} is inside`) : no(`${ctx.dst || 'unknown address'} is outside`)
  }
  return no('unsupported')
}

/** Evaluates a tree: { value, reason } where reason tells which atom decided. */
export function explain(node, ctx) {
  switch (node.type) {
    case 'atom':
      return atomResult(node, ctx)
    case 'not': {
      const r = explain(node.x, ctx)
      return { value: !r.value, reason: `! (${r.reason})` }
    }
    case 'and': {
      const l = explain(node.l, ctx)
      if (!l.value) return l
      const r = explain(node.r, ctx)
      return { value: r.value, reason: r.value ? `${l.reason}; ${r.reason}` : r.reason }
    }
    case 'or': {
      const l = explain(node.l, ctx)
      if (l.value) return l
      const r = explain(node.r, ctx)
      return { value: r.value, reason: r.value ? r.reason : `${l.reason}; ${r.reason}` }
    }
  }
  return { value: false, reason: '' }
}

export const matches = (node, ctx) => explain(node, ctx).value
