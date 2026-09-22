import { NextResponse } from 'next/server'
import fs from 'fs'
import path from 'path'
import { CODE_DIR } from '@/lib/code-dir'

function resolveWithin(baseDir: string, ...segments: string[]): string | null {
  const resolvedBase = path.resolve(baseDir)
  const resolvedTarget = path.resolve(resolvedBase, ...segments)
  const prefix = resolvedBase.endsWith(path.sep) ? resolvedBase : resolvedBase + path.sep
  if (!resolvedTarget.startsWith(prefix) && resolvedTarget !== resolvedBase) {
    return null
  }
  return resolvedTarget
}

interface ReportDetail {
  filename: string
  tool: string
  task: string
  date: string
  grade: number | null
  size: number
}

// Known tool names for format detection
const KNOWN_TOOLS = ['claude', 'gemini', 'codex', 'opencode', 'kilocode']

// Supports both old and new filename formats:
// Old: {tool}-{codebase}-{task}-{date}.md (e.g., claude-dispatch-audit-2026-01-16_2331.md)
// New: {codebase}-{tool}-{task}-{date}.md (e.g., dispatch-claude-audit-2026-01-20_2204.md)
function parseReportFilename(filename: string): { tool: string; codebase: string; task: string; date: string } | null {
  const match = filename.match(/^(.+)-([a-z]+)-([a-z]+)-(\d{4}-\d{2}-\d{2}_\d{4})\.md$/)
  if (!match) return null

  const segment1 = match[1]
  const segment2 = match[2]
  const segment3 = match[3]
  const date = match[4]

  // Detect format by checking if segment1 is a known tool (old format)
  if (KNOWN_TOOLS.includes(segment1.toLowerCase())) {
    return { tool: segment1, codebase: segment2, task: segment3, date }
  } else if (KNOWN_TOOLS.includes(segment2.toLowerCase())) {
    return { codebase: segment1, tool: segment2, task: segment3, date }
  }

  return { codebase: segment1, tool: segment2, task: segment3, date }
}

function parseDate(dateStr: string): Date {
  const [datePart, timePart] = dateStr.split('_')
  const hours = timePart.slice(0, 2)
  const minutes = timePart.slice(2, 4)
  return new Date(`${datePart}T${hours}:${minutes}:00`)
}

function extractGrade(content: string): number | null {
  const patterns = [
    /TOTAL_SCORE:\s*(\d+(?:\.\d+)?)\s*\/\s*100/i,
    /Overall Grade[^:]*:\s*(\d+(?:\.\d+)?)\s*\/\s*100/i,
    /Grade[^:]*:\s*(\d+(?:\.\d+)?)\s*\/\s*100/i,
    /(\d+(?:\.\d+)?)\s*\/\s*100\s*(?:points?)?/i
  ]

  for (const pattern of patterns) {
    const match = content.match(pattern)
    if (match) {
      return parseFloat(match[1])
    }
  }
  return null
}

export async function GET(
  request: Request,
  { params }: { params: Promise<{ name: string }> }
) {
  const { name } = await params
  const repoPath = resolveWithin(CODE_DIR, name)
  if (!repoPath) {
    return NextResponse.json({ error: 'Invalid repo path' }, { status: 400 })
  }
  const rcodgenDir = path.join(repoPath, '_rcodegen')

  if (!fs.existsSync(rcodgenDir)) {
    return NextResponse.json({ error: 'Repo not found' }, { status: 404 })
  }

  try {
    const files = fs.readdirSync(rcodgenDir)
    const reports: ReportDetail[] = []

    for (const file of files) {
      if (!file.endsWith('.md')) continue

      const parsed = parseReportFilename(file)
      if (!parsed) continue

      const filePath = path.join(rcodgenDir, file)
      const stats = fs.statSync(filePath)
      const content = fs.readFileSync(filePath, 'utf-8')
      const grade = extractGrade(content)

      reports.push({
        filename: file,
        tool: parsed.tool,
        task: parsed.task,
        date: parseDate(parsed.date).toISOString(),
        grade,
        size: stats.size
      })
    }

    // Sort by date descending
    reports.sort((a, b) => new Date(b.date).getTime() - new Date(a.date).getTime())

    // Group by task
    const grouped: Record<string, ReportDetail[]> = {}
    for (const report of reports) {
      if (!grouped[report.task]) {
        grouped[report.task] = []
      }
      grouped[report.task].push(report)
    }

    return NextResponse.json({
      name,
      path: repoPath,
      reports,
      grouped
    })
  } catch (error) {
    console.error('Error reading repo:', error)
    return NextResponse.json({ error: 'Failed to read repo' }, { status: 500 })
  }
}
