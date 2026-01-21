import { NextResponse } from 'next/server'
import fs from 'fs/promises'
import fsSync from 'fs'
import path from 'path'
import os from 'os'

// Configurable code directory via environment variable
const CODE_DIR = process.env.RCODEGEN_CODE_DIR || path.join(os.homedir(), 'Desktop/_code')

const PRIMARY_TASKS = ['audit', 'test', 'fix', 'refactor'] as const

interface TaskGradeInfo {
  grade: number | null
  tool: string | null
}

interface TaskGrades {
  audit: TaskGradeInfo[]
  test: TaskGradeInfo[]
  fix: TaskGradeInfo[]
  refactor: TaskGradeInfo[]
}

interface GradeHistoryPoint {
  date: Date
  grade: number
  tool: string
}

interface TaskHistory {
  audit: GradeHistoryPoint[]
  test: GradeHistoryPoint[]
  fix: GradeHistoryPoint[]
  refactor: GradeHistoryPoint[]
}

interface ReportSummary {
  filename: string
  tool: string
  task: string
  date: Date
  grade: number | null
  dateUpdated: Date | null
}

interface TaskLastUpdated {
  audit: Date | null  // shared with fix
  test: Date | null
  fix: Date | null    // shared with audit
  refactor: Date | null
}

interface RepoSummary {
  name: string
  path: string
  reportCount: number
  pendingCount: number
  lastRun: Date | null
  latestGrade: number | null
  taskGrades: TaskGrades
  gradeHistory: TaskHistory
  taskLastUpdated: TaskLastUpdated
  reports: ReportSummary[]
}

// Structure of .grades.json file
interface GradeEntry {
  date: string
  tool: string
  task: string
  grade: number
  reportFile: string
}

interface GradesFile {
  grades: GradeEntry[]
}

// Validate GradeEntry structure
function isValidGradeEntry(entry: unknown): entry is GradeEntry {
  if (typeof entry !== 'object' || entry === null) return false
  const e = entry as Record<string, unknown>
  return (
    typeof e.date === 'string' &&
    typeof e.tool === 'string' &&
    typeof e.task === 'string' &&
    typeof e.grade === 'number' &&
    typeof e.reportFile === 'string'
  )
}

// Validate GradesFile structure
function isValidGradesFile(data: unknown): data is GradesFile {
  if (typeof data !== 'object' || data === null) return false
  const d = data as Record<string, unknown>
  if (!Array.isArray(d.grades)) return false
  return d.grades.every(isValidGradeEntry)
}

// Load grades from .grades.json file with validation
function loadGradesFile(rcodgenDir: string): GradesFile | null {
  const gradesPath = path.join(rcodgenDir, '.grades.json')
  try {
    if (fsSync.existsSync(gradesPath)) {
      const content = fsSync.readFileSync(gradesPath, 'utf-8')
      const data = JSON.parse(content)
      if (isValidGradesFile(data)) {
        return data
      }
      console.warn(`Invalid grades file structure in ${gradesPath}`)
    }
  } catch (err) {
    console.warn(`Error loading grades file: ${err}`)
  }
  return null
}

// Save grades to .grades.json file atomically
function saveGradesFile(rcodgenDir: string, grades: GradesFile): boolean {
  const gradesPath = path.join(rcodgenDir, '.grades.json')
  const tempPath = gradesPath + '.tmp'
  try {
    const content = JSON.stringify(grades, null, 2)
    // Write to temp file first
    fsSync.writeFileSync(tempPath, content, 'utf-8')
    // Atomic rename
    fsSync.renameSync(tempPath, gradesPath)
    return true
  } catch (err) {
    // Clean up temp file on failure
    try { fsSync.unlinkSync(tempPath) } catch { /* ignore */ }
    console.error(`Error saving grades file: ${err}`)
    return false
  }
}

// Migrate grades for a single repo - backfill any missing entries
// This is NOT called automatically on GET - only via POST
function migrateRepoGrades(rcodgenDir: string): number {
  const gradesFile = loadGradesFile(rcodgenDir) || { grades: [] }
  const existingFiles = new Set(gradesFile.grades.map(g => g.reportFile))

  let files: string[]
  try {
    files = fsSync.readdirSync(rcodgenDir)
  } catch {
    return 0
  }

  let added = 0

  for (const file of files) {
    if (!file.endsWith('.md')) continue
    if (existingFiles.has(file)) continue

    const parsed = parseReportFilename(file)
    if (!parsed) continue

    const filePath = path.join(rcodgenDir, file)
    let content: string
    try {
      content = fsSync.readFileSync(filePath, 'utf-8')
    } catch {
      continue
    }
    const grade = extractGrade(content)

    if (grade !== null) {
      const date = parseDate(parsed.date)
      gradesFile.grades.push({
        date: date.toISOString(),
        tool: parsed.tool.toLowerCase(),
        task: parsed.task.toLowerCase(),
        grade,
        reportFile: file
      })
      added++
    }
  }

  if (added > 0) {
    // Sort by date using proper date comparison
    gradesFile.grades.sort((a, b) => new Date(a.date).getTime() - new Date(b.date).getTime())
    saveGradesFile(rcodgenDir, gradesFile)
  }

  return added
}

// Known tool names for format detection
const KNOWN_TOOLS = ['claude', 'gemini', 'codex']

// Supports various filename formats with flexible date patterns:
// Standard: {codebase}-{tool}-{task}-{YYYY-MM-DD_HHMM}.md
// With seconds: {codebase}-{tool}-{task}-{YYYY-MM-DD_HHMMSS}.md
// Date only: {codebase}-{tool}-{task}-{YYYY-MM-DD}.md
// Compact: {codebase}-{tool}-{task}-{YYYYMMDD-HHMMSS}.md
function parseReportFilename(filename: string): { tool: string; codebase: string; task: string; date: string } | null {
  // Flexible pattern that handles various date formats
  const match = filename.match(/^(.+)-([a-z]+)-([a-z]+)-(\d{4}-?\d{2}-?\d{2}(?:[_-]\d{4,6})?)\.md$/)
  if (!match) return null

  const segment1 = match[1]
  const segment2 = match[2]
  const segment3 = match[3]
  const date = match[4]

  // Detect format by checking if segment1 is a known tool (old format)
  // or if segment2 is a known tool (new format)
  if (KNOWN_TOOLS.includes(segment1.toLowerCase())) {
    // Old format: {tool}-{codebase}-{task}
    return { tool: segment1, codebase: segment2, task: segment3, date }
  } else if (KNOWN_TOOLS.includes(segment2.toLowerCase())) {
    // New format: {codebase}-{tool}-{task}
    return { codebase: segment1, tool: segment2, task: segment3, date }
  }

  // Fallback: assume new format (codebase first)
  return { codebase: segment1, tool: segment2, task: segment3, date }
}

// Parse date with flexible format support
function parseDate(dateStr: string): Date {
  // Try to parse various formats

  // Check for underscore separator (YYYY-MM-DD_HHMM or YYYY-MM-DD_HHMMSS)
  if (dateStr.includes('_')) {
    const [datePart, timePart] = dateStr.split('_')
    const hours = timePart.slice(0, 2)
    const minutes = timePart.slice(2, 4)
    const seconds = timePart.length >= 6 ? timePart.slice(4, 6) : '00'
    return new Date(`${datePart}T${hours}:${minutes}:${seconds}Z`)
  }

  // Check for compact format with dash (YYYYMMDD-HHMMSS)
  const compactMatch = dateStr.match(/^(\d{4})(\d{2})(\d{2})-(\d{2})(\d{2})(\d{2})?$/)
  if (compactMatch) {
    const [, year, month, day, hours, minutes, seconds = '00'] = compactMatch
    return new Date(`${year}-${month}-${day}T${hours}:${minutes}:${seconds}Z`)
  }

  // Date only formats
  const dateOnlyMatch = dateStr.match(/^(\d{4})-?(\d{2})-?(\d{2})$/)
  if (dateOnlyMatch) {
    const [, year, month, day] = dateOnlyMatch
    return new Date(`${year}-${month}-${day}T00:00:00Z`)
  }

  // Fallback: try native parsing
  return new Date(dateStr + 'T00:00:00Z')
}

// Extract Date Updated from file content
function extractDateUpdated(content: string): Date | null {
  // Pattern: Date Updated: YYYY-MM-DD
  const match = content.match(/Date Updated:\s*(\d{4}-\d{2}-\d{2})/)
  if (match) {
    return new Date(match[1] + 'T00:00:00Z')
  }
  return null
}

// More specific grade extraction patterns
function extractGrade(content: string): number | null {
  // Look for patterns like "TOTAL_SCORE: 77.1/100" or "Overall Grade: 77/100"
  // Ordered from most specific to least specific
  const patterns = [
    /TOTAL_SCORE:\s*(\d+(?:\.\d+)?)\s*\/\s*100/i,
    /Overall Grade[^:]*:\s*(\d+(?:\.\d+)?)\s*\/\s*100/i,
    /(?:Final |Code |Test |Quality )?Grade[^:]*:\s*(\d+(?:\.\d+)?)\s*\/\s*100/i,
    /(?:Total |Final )?(?:Score|Rating|Points?)[:=]\s*(\d+(?:\.\d+)?)\s*\/\s*100/i,
  ]

  for (const pattern of patterns) {
    const match = content.match(pattern)
    if (match) {
      const grade = parseFloat(match[1])
      // Validate grade range
      if (grade >= 0 && grade <= 100) {
        return grade
      }
    }
  }
  return null
}

function scanRepo(repoPath: string): RepoSummary | null {
  const rcodgenDir = path.join(repoPath, '_rcodegen')

  if (!fsSync.existsSync(rcodgenDir)) {
    return null
  }

  // Load grades from .grades.json (NO auto-migration on GET)
  const gradesFile = loadGradesFile(rcodgenDir)
  const gradesFromFile = new Map<string, GradeEntry>()
  if (gradesFile) {
    for (const entry of gradesFile.grades) {
      gradesFromFile.set(entry.reportFile, entry)
    }
  }

  let files: string[]
  try {
    files = fsSync.readdirSync(rcodgenDir)
  } catch {
    return null
  }

  const reports: ReportSummary[] = []
  let pendingCount = 0

  for (const file of files) {
    if (!file.endsWith('.md')) continue

    const parsed = parseReportFilename(file)
    if (!parsed) continue

    const filePath = path.join(rcodgenDir, file)
    let content: string | null = null

    // Try to get grade from .grades.json first, fall back to file extraction
    let grade: number | null = null
    const cachedGrade = gradesFromFile.get(file)
    if (cachedGrade) {
      grade = cachedGrade.grade
    } else {
      // Fall back to extracting from file
      try {
        content = fsSync.readFileSync(filePath, 'utf-8')
        grade = extractGrade(content)
      } catch {
        // Skip files that can't be read
        continue
      }
    }

    // Check if file has been processed (contains "Date Updated")
    if (content === null) {
      try {
        content = fsSync.readFileSync(filePath, 'utf-8')
      } catch {
        // If we can't read it, count as pending
        pendingCount++
      }
    }

    let dateUpdated: Date | null = null
    if (content !== null) {
      if (!content.includes('Date Updated')) {
        pendingCount++
      } else {
        dateUpdated = extractDateUpdated(content)
      }
    }

    reports.push({
      filename: file,
      tool: parsed.tool,
      task: parsed.task,
      date: parseDate(parsed.date),
      grade,
      dateUpdated
    })
  }

  // Also include any grades from .grades.json that don't have corresponding files
  // (files may have been deleted but grades preserved)
  const existingFiles = new Set(files)
  if (gradesFile) {
    for (const entry of gradesFile.grades) {
      if (!existingFiles.has(entry.reportFile)) {
        // Parse the filename to get task info
        const parsed = parseReportFilename(entry.reportFile)
        if (parsed) {
          reports.push({
            filename: entry.reportFile,
            tool: entry.tool,
            task: entry.task,
            date: new Date(entry.date),
            grade: entry.grade,
            dateUpdated: null  // file doesn't exist, so no dateUpdated
          })
        }
      }
    }
  }

  // Sort by date descending
  reports.sort((a, b) => b.date.getTime() - a.date.getTime())

  // Find latest grade from any report
  const latestGrade = reports.find(r => r.grade !== null)?.grade ?? null

  // Compute per-task grades with tool info (one per unique tool)
  const taskGrades: TaskGrades = {
    audit: [],
    test: [],
    fix: [],
    refactor: []
  }
  for (const task of PRIMARY_TASKS) {
    const seenTools = new Set<string>()
    for (const report of reports) {
      if (report.task === task && report.grade !== null && !seenTools.has(report.tool)) {
        seenTools.add(report.tool)
        taskGrades[task].push({ grade: report.grade, tool: report.tool })
      }
    }
  }

  // Compute grade history per task (all graded reports, sorted by date ascending)
  const gradeHistory: TaskHistory = {
    audit: [],
    test: [],
    fix: [],
    refactor: []
  }
  for (const task of PRIMARY_TASKS) {
    const taskReports = reports
      .filter(r => r.task === task && r.grade !== null)
      .sort((a, b) => a.date.getTime() - b.date.getTime())
    gradeHistory[task] = taskReports.map(r => ({ date: r.date, grade: r.grade as number, tool: r.tool }))
  }

  // Compute most recent dateUpdated per task
  // Note: audit and fix share the same date (take latest from either)
  const findLatestDateUpdated = (tasks: string[]): Date | null => {
    let latest: Date | null = null
    for (const report of reports) {
      if (tasks.includes(report.task) && report.dateUpdated) {
        if (!latest || report.dateUpdated.getTime() > latest.getTime()) {
          latest = report.dateUpdated
        }
      }
    }
    return latest
  }

  const auditFixLatest = findLatestDateUpdated(['audit', 'fix'])
  const taskLastUpdated: TaskLastUpdated = {
    audit: auditFixLatest,
    test: findLatestDateUpdated(['test']),
    fix: auditFixLatest,
    refactor: findLatestDateUpdated(['refactor'])
  }

  return {
    name: path.basename(repoPath),
    path: repoPath,
    reportCount: reports.length,
    pendingCount,
    lastRun: reports.length > 0 ? reports[0].date : null,
    latestGrade,
    taskGrades,
    gradeHistory,
    taskLastUpdated,
    reports
  }
}

export async function GET() {
  try {
    // Use async stat for checking directory
    try {
      await fs.stat(CODE_DIR)
    } catch {
      return NextResponse.json({ error: 'Code directory not found', path: CODE_DIR }, { status: 404 })
    }

    const entries = await fs.readdir(CODE_DIR, { withFileTypes: true })
    const repos: RepoSummary[] = []

    for (const entry of entries) {
      if (!entry.isDirectory()) continue
      if (entry.name.startsWith('.')) continue

      const repoPath = path.join(CODE_DIR, entry.name)
      const summary = scanRepo(repoPath)

      if (summary && summary.reportCount > 0) {
        repos.push(summary)
      }
    }

    // Sort by last run date descending
    repos.sort((a, b) => {
      if (!a.lastRun && !b.lastRun) return 0
      if (!a.lastRun) return 1
      if (!b.lastRun) return -1
      return b.lastRun.getTime() - a.lastRun.getTime()
    })

    return NextResponse.json(repos)
  } catch (error) {
    console.error('Error scanning repos:', error)
    return NextResponse.json({ error: 'Failed to scan repos' }, { status: 500 })
  }
}

// POST /api/repos - Trigger grade migration for all repos
export async function POST() {
  try {
    try {
      await fs.stat(CODE_DIR)
    } catch {
      return NextResponse.json({ error: 'Code directory not found', path: CODE_DIR }, { status: 404 })
    }

    const entries = await fs.readdir(CODE_DIR, { withFileTypes: true })
    const results: { repo: string; added: number }[] = []
    let totalAdded = 0

    for (const entry of entries) {
      if (!entry.isDirectory()) continue
      if (entry.name.startsWith('.')) continue

      const repoPath = path.join(CODE_DIR, entry.name)
      const rcodgenDir = path.join(repoPath, '_rcodegen')

      if (!fsSync.existsSync(rcodgenDir)) continue

      const added = migrateRepoGrades(rcodgenDir)
      if (added > 0) {
        results.push({ repo: entry.name, added })
        totalAdded += added
      }
    }

    return NextResponse.json({
      success: true,
      message: `Migrated ${totalAdded} grades across ${results.length} repos`,
      totalAdded,
      repos: results
    })
  } catch (error) {
    console.error('Error migrating grades:', error)
    return NextResponse.json({ error: 'Failed to migrate grades' }, { status: 500 })
  }
}
