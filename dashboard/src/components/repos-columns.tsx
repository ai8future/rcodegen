"use client"

import { ColumnDef } from "@tanstack/react-table"
import Link from "next/link"
import { useRowExpansion } from "@/components/ui/data-table"

export interface TaskGradeInfo {
  grade: number | null
  tool: string | null
}

export interface TaskGrades {
  audit: TaskGradeInfo[]
  test: TaskGradeInfo[]
  fix: TaskGradeInfo[]
  refactor: TaskGradeInfo[]
}

export interface GradeHistoryPoint {
  date: string
  grade: number
  tool: string
}

export interface TaskHistory {
  audit: GradeHistoryPoint[]
  test: GradeHistoryPoint[]
  fix: GradeHistoryPoint[]
  refactor: GradeHistoryPoint[]
}

interface TaskLastUpdated {
  audit: string | null
  test: string | null
  fix: string | null
  refactor: string | null
}

export interface RepoSummary {
  name: string
  path: string
  reportCount: number
  pendingCount: number
  lastRun: string | null
  latestGrade: number | null
  taskGrades: TaskGrades
  gradeHistory: TaskHistory
  taskLastUpdated: TaskLastUpdated
}

// Task colors - distinct from grade colors
export const TASK_COLORS = {
  audit: "#8b5cf6",    // Purple
  test: "#06b6d4",     // Cyan
  fix: "#ec4899",      // Pink
  refactor: "#f59e0b", // Amber
} as const

// Colors for different tools
const TOOL_COLORS: Record<string, string> = {
  gemini: "#4285f4",  // Blue
  codex: "#22c55e",   // Green
  claude: "#ef4444",  // Red
}

function MiniSparkline({ points, color }: { points: GradeHistoryPoint[]; color: string }) {
  if (points.length < 1) return null

  const width = 160
  const height = 68
  const paddingLeft = 38  // Room for model name label
  const paddingRight = 14
  const paddingTop = 14
  const paddingBottom = 22

  // Sort all points by date
  const sortedPoints = [...points].sort((a, b) =>
    new Date(a.date).getTime() - new Date(b.date).getTime()
  )

  // Group points by tool
  const toolGroups: Record<string, GradeHistoryPoint[]> = {}
  for (const p of sortedPoints) {
    const tool = p.tool.toLowerCase()
    if (!toolGroups[tool]) toolGroups[tool] = []
    toolGroups[tool].push(p)
  }

  const tools = Object.keys(toolGroups)
  if (tools.length === 0) return null

  const grades = points.map(p => p.grade)
  const minGrade = Math.min(...grades) - 5
  const maxGrade = Math.max(...grades) + 5
  const range = maxGrade - minGrade || 1

  const dates = points.map(p => new Date(p.date).getTime())
  const minDate = Math.min(...dates)
  const maxDate = Math.max(...dates)
  const dateRange = maxDate - minDate || 1

  const scaleX = (date: string) => {
    const t = new Date(date).getTime()
    if (dateRange === 0) return paddingLeft + (width - paddingLeft - paddingRight) / 2
    return paddingLeft + ((t - minDate) / dateRange) * (width - paddingLeft - paddingRight)
  }

  const scaleY = (grade: number) => {
    return paddingTop + ((maxGrade - grade) / range) * (height - paddingTop - paddingBottom)
  }

  const formatDate = (date: string) => {
    const d = new Date(date)
    return `${d.getMonth() + 1}/${d.getDate()}`
  }

  const formatTime = (date: string) => {
    const d = new Date(date)
    return `${d.getHours().toString().padStart(2, '0')}:${d.getMinutes().toString().padStart(2, '0')}`
  }

  // Pre-calculate tool label positions and resolve collisions
  const labelMinSpacing = 9 // Minimum vertical spacing between labels
  const toolLabelPositions: { tool: string; y: number }[] = tools.map(tool => {
    const toolPoints = toolGroups[tool]
    const baseY = toolPoints.length > 0 ? scaleY(toolPoints[0].grade) + 2 : height / 2
    return { tool, y: baseY }
  })

  // Sort by Y position and adjust overlapping labels
  toolLabelPositions.sort((a, b) => a.y - b.y)
  for (let i = 1; i < toolLabelPositions.length; i++) {
    const prev = toolLabelPositions[i - 1]
    const curr = toolLabelPositions[i]
    if (curr.y - prev.y < labelMinSpacing) {
      curr.y = prev.y + labelMinSpacing
    }
  }

  // Create a lookup map for adjusted Y positions
  const labelYMap = new Map(toolLabelPositions.map(p => [p.tool, p.y]))

  // Collect all score label positions across all tools to detect collisions
  const allScoreLabels: { x: number; y: number; grade: number; tool: string }[] = []
  for (const tool of tools) {
    for (const p of toolGroups[tool]) {
      allScoreLabels.push({
        x: scaleX(p.date),
        y: scaleY(p.grade) - 4,
        grade: p.grade,
        tool
      })
    }
  }

  // Adjust score labels that would collide (within 12px horizontal and 8px vertical)
  const scoreMinSpacingX = 12
  const scoreMinSpacingY = 8
  for (let i = 0; i < allScoreLabels.length; i++) {
    for (let j = i + 1; j < allScoreLabels.length; j++) {
      const a = allScoreLabels[i]
      const b = allScoreLabels[j]
      const dx = Math.abs(a.x - b.x)
      const dy = Math.abs(a.y - b.y)
      if (dx < scoreMinSpacingX && dy < scoreMinSpacingY) {
        // Push the lower one down
        if (a.y <= b.y) {
          b.y = a.y + scoreMinSpacingY
        } else {
          a.y = b.y + scoreMinSpacingY
        }
      }
    }
  }

  // Create lookup for adjusted score positions
  const scoreLabelMap = new Map(
    allScoreLabels.map(s => [`${s.tool}-${s.grade}-${Math.round(scaleX(sortedPoints.find(p => p.tool.toLowerCase() === s.tool && p.grade === s.grade)?.date || ''))}`, s.y])
  )

  return (
    <svg width={width} height={height} className="mt-1">
      {/* Draw line and points for each tool */}
      {tools.map((tool) => {
        const toolPoints = toolGroups[tool]
        const toolColor = TOOL_COLORS[tool] || color
        const toolName = tool.charAt(0).toUpperCase() + tool.slice(1)

        // Draw line if more than 1 point
        const pathData = toolPoints.length > 1
          ? toolPoints.map((p, i) => `${i === 0 ? 'M' : 'L'} ${scaleX(p.date)} ${scaleY(p.grade)}`).join(' ')
          : null

        // Get the adjusted Y position for the tool name label
        const labelY = labelYMap.get(tool) || height / 2

        return (
          <g key={tool}>
            {/* Tool name label on the left */}
            <text x={2} y={labelY} fill={toolColor} fontSize="7" fontWeight="bold" textAnchor="start">
              {toolName}
            </text>
            {pathData && (
              <path
                d={pathData}
                fill="none"
                stroke={toolColor}
                strokeWidth={1.5}
                strokeLinecap="round"
                strokeLinejoin="round"
                opacity={0.8}
              />
            )}
            {/* Dots, score labels, and date labels */}
            {toolPoints.map((p, i) => {
              const x = scaleX(p.date)
              const y = scaleY(p.grade)
              // Look up adjusted score Y position
              const scoreKey = `${tool}-${p.grade}-${Math.round(x)}`
              const scoreY = scoreLabelMap.get(scoreKey) ?? (y - 4)
              const dateY = height - 9
              const timeY = height - 2
              return (
                <g key={i}>
                  <circle cx={x} cy={y} r={2.5} fill={toolColor} />
                  <text x={x} y={scoreY} fill={toolColor} fontSize="7" fontWeight="bold" textAnchor="middle">
                    {Math.round(p.grade)}
                  </text>
                  <text x={x} y={dateY} fill="currentColor" className="text-muted-foreground" fontSize="6" textAnchor="middle">
                    {formatDate(p.date)}
                  </text>
                  <text x={x} y={timeY} fill="currentColor" className="text-muted-foreground" fontSize="5" textAnchor="middle">
                    {formatTime(p.date)}
                  </text>
                </g>
              )
            })}
          </g>
        )
      })}
    </svg>
  )
}

// Larger sparkline for expanded view
function LargeSparkline({ points, color, title }: { points: GradeHistoryPoint[]; color: string; title: string }) {
  if (points.length < 1) {
    return (
      <div className="flex flex-col items-center justify-center h-full text-muted-foreground">
        <span className="text-sm">No data</span>
      </div>
    )
  }

  const width = 280
  const height = 180
  const paddingLeft = 50
  const paddingRight = 20
  const paddingTop = 35
  const paddingBottom = 45

  const sortedPoints = [...points].sort((a, b) =>
    new Date(a.date).getTime() - new Date(b.date).getTime()
  )

  const toolGroups: Record<string, GradeHistoryPoint[]> = {}
  for (const p of sortedPoints) {
    const tool = p.tool.toLowerCase()
    if (!toolGroups[tool]) toolGroups[tool] = []
    toolGroups[tool].push(p)
  }

  const tools = Object.keys(toolGroups)
  if (tools.length === 0) return null

  const grades = points.map(p => p.grade)
  const minGrade = Math.min(...grades) - 5
  const maxGrade = Math.max(...grades) + 5
  const range = maxGrade - minGrade || 1

  const dates = points.map(p => new Date(p.date).getTime())
  const minDate = Math.min(...dates)
  const maxDate = Math.max(...dates)
  const dateRange = maxDate - minDate || 1

  const scaleX = (date: string) => {
    const t = new Date(date).getTime()
    if (dateRange === 0) return paddingLeft + (width - paddingLeft - paddingRight) / 2
    return paddingLeft + ((t - minDate) / dateRange) * (width - paddingLeft - paddingRight)
  }

  const scaleY = (grade: number) => {
    return paddingTop + ((maxGrade - grade) / range) * (height - paddingTop - paddingBottom)
  }

  const formatDate = (date: string) => {
    const d = new Date(date)
    return `${d.getMonth() + 1}/${d.getDate()}`
  }

  const formatTime = (date: string) => {
    const d = new Date(date)
    return `${d.getHours().toString().padStart(2, '0')}:${d.getMinutes().toString().padStart(2, '0')}`
  }

  // Y-axis grid lines
  const gridLines = [100, 90, 80, 70, 60, 50].filter(v => v >= minGrade && v <= maxGrade)

  return (
    <div className="flex flex-col">
      <h3 className="text-sm font-semibold text-foreground mb-2 capitalize" style={{ color }}>{title}</h3>
      <svg width={width} height={height}>
        {/* Y-axis grid lines */}
        {gridLines.map(grade => (
          <g key={grade}>
            <line
              x1={paddingLeft}
              y1={scaleY(grade)}
              x2={width - paddingRight}
              y2={scaleY(grade)}
              stroke="currentColor"
              className="text-border"
              strokeDasharray="2,2"
              opacity={0.5}
            />
            <text
              x={paddingLeft - 8}
              y={scaleY(grade) + 3}
              fill="currentColor"
              className="text-muted-foreground"
              fontSize="10"
              textAnchor="end"
            >
              {grade}
            </text>
          </g>
        ))}

        {/* Draw lines and points for each tool */}
        {tools.map((tool) => {
          const toolPoints = toolGroups[tool]
          const toolColor = TOOL_COLORS[tool] || color
          const toolName = tool.charAt(0).toUpperCase() + tool.slice(1)

          const pathData = toolPoints.length > 1
            ? toolPoints.map((p, i) => `${i === 0 ? 'M' : 'L'} ${scaleX(p.date)} ${scaleY(p.grade)}`).join(' ')
            : null

          return (
            <g key={tool}>
              {pathData && (
                <path
                  d={pathData}
                  fill="none"
                  stroke={toolColor}
                  strokeWidth={2}
                  strokeLinecap="round"
                  strokeLinejoin="round"
                  opacity={0.9}
                />
              )}
              {toolPoints.map((p, i) => {
                const x = scaleX(p.date)
                const y = scaleY(p.grade)
                return (
                  <g key={i}>
                    <circle cx={x} cy={y} r={5} fill={toolColor} />
                    <text x={x} y={y - 10} fill={toolColor} fontSize="11" fontWeight="bold" textAnchor="middle">
                      {Math.round(p.grade)}
                    </text>
                    <text x={x} y={height - 22} fill="currentColor" className="text-muted-foreground" fontSize="9" textAnchor="middle">
                      {formatDate(p.date)}
                    </text>
                    <text x={x} y={height - 10} fill="currentColor" className="text-muted-foreground" fontSize="8" textAnchor="middle">
                      {formatTime(p.date)}
                    </text>
                  </g>
                )
              })}
            </g>
          )
        })}

        {/* Legend */}
        {tools.map((tool, i) => {
          const toolColor = TOOL_COLORS[tool] || color
          const toolName = tool.charAt(0).toUpperCase() + tool.slice(1)
          return (
            <g key={tool} transform={`translate(${paddingLeft + i * 70}, 12)`}>
              <circle cx={0} cy={0} r={4} fill={toolColor} />
              <text x={8} y={4} fill={toolColor} fontSize="10" fontWeight="bold">
                {toolName}
              </text>
            </g>
          )
        })}
      </svg>
    </div>
  )
}

// Expanded panel showing all 4 graphs larger
export function ExpandedGraphsPanel({
  gradeHistory,
  onClose
}: {
  gradeHistory: TaskHistory
  onClose: () => void
}) {
  return (
    <div
      className="bg-muted/30 px-6 py-4 cursor-pointer hover:bg-muted/40 transition-colors"
      onClick={onClose}
    >
      <div className="flex items-center justify-between mb-3">
        <span className="text-xs text-muted-foreground">Click anywhere to collapse</span>
      </div>
      <div className="grid grid-cols-4 gap-4">
        <LargeSparkline points={gradeHistory.audit} color={TASK_COLORS.audit} title="Audit" />
        <LargeSparkline points={gradeHistory.test} color={TASK_COLORS.test} title="Test" />
        <LargeSparkline points={gradeHistory.fix} color={TASK_COLORS.fix} title="Fix" />
        <LargeSparkline points={gradeHistory.refactor} color={TASK_COLORS.refactor} title="Refactor" />
      </div>
    </div>
  )
}

function formatRelativeTime(dateStr: string | null): string {
  if (!dateStr) return "-"
  const date = new Date(dateStr)
  const now = new Date()
  const diffMs = now.getTime() - date.getTime()
  const diffMins = Math.floor(diffMs / 60000)
  const diffHours = Math.floor(diffMs / 3600000)
  const diffDays = Math.floor(diffMs / 86400000)

  if (diffMins < 1) return "just now"
  if (diffMins < 60) return `${diffMins}m ago`
  if (diffHours < 24) return `${diffHours}h ago`
  return `${diffDays}d ago`
}

function formatToolName(tool: string | null): string {
  if (!tool) return ""
  return tool.charAt(0).toUpperCase() + tool.slice(1)
}

function getGradeColor(grade: number): string {
  if (grade >= 90) return "bg-green-100 text-green-800 dark:bg-green-900/30 dark:text-green-400"
  if (grade >= 80) return "bg-blue-100 text-blue-800 dark:bg-blue-900/30 dark:text-blue-400"
  if (grade >= 70) return "bg-yellow-100 text-yellow-800 dark:bg-yellow-900/30 dark:text-yellow-400"
  if (grade >= 60) return "bg-orange-100 text-orange-800 dark:bg-orange-900/30 dark:text-orange-400"
  return "bg-red-100 text-red-800 dark:bg-red-900/30 dark:text-red-400"
}

function formatLastUpdatedAge(dateStr: string | null): string {
  if (!dateStr) return ""
  const date = new Date(dateStr)
  const now = new Date()
  const diffMs = now.getTime() - date.getTime()
  const diffDays = Math.floor(diffMs / 86400000)

  if (diffDays === 0) return "today"
  if (diffDays === 1) return "1d ago"
  if (diffDays < 7) return `${diffDays}d ago`
  if (diffDays < 30) return `${Math.floor(diffDays / 7)}w ago`
  return `${Math.floor(diffDays / 30)}mo ago`
}

function TaskGradeCell({ grades, history, color, lastUpdated, rowId }: { grades: TaskGradeInfo[]; history: GradeHistoryPoint[]; color: string; lastUpdated: string | null; rowId: string }) {
  const { expandedRowId, toggleRowExpansion } = useRowExpansion()
  const isExpanded = expandedRowId === rowId
  const validGrades = grades.filter(g => g.grade !== null)
  if (validGrades.length === 0) return <span className="text-muted-foreground">-</span>

  const avg = validGrades.reduce((sum, g) => sum + (g.grade as number), 0) / validGrades.length
  const colorClass = getGradeColor(avg)
  const lastUpdatedAge = formatLastUpdatedAge(lastUpdated)

  return (
    <div
      className={`flex flex-col items-center gap-0.5 cursor-pointer rounded-md p-1 transition-colors ${isExpanded ? 'bg-primary/10' : 'hover:bg-muted/50'}`}
      onClick={() => toggleRowExpansion(rowId)}
      title="Click to expand graphs"
    >
      <span className={`px-2 py-1 rounded text-sm font-bold ${colorClass}`}>
        {Math.round(avg)}
      </span>
      <div className="flex flex-col items-center gap-0">
        {validGrades.map((g, i) => {
          const gradeColor = getGradeColor(g.grade as number).split(" ").slice(1).join(" ")
          return (
            <span key={i} className="text-[10px]">
              <span className="text-muted-foreground">{formatToolName(g.tool)}:</span>{" "}
              <span className={`font-bold ${gradeColor}`}>{g.grade}</span>
            </span>
          )
        })}
      </div>
      <MiniSparkline points={history} color={color} />
      {lastUpdatedAge && (
        <span className="text-[9px] text-purple-600 dark:text-purple-400 font-bold mt-0.5">
          {lastUpdatedAge}
        </span>
      )}
    </div>
  )
}

function SortableHeader({ column, children, className }: { column: any; children: React.ReactNode; className?: string }) {
  const sorted = column.getIsSorted()
  return (
    <button
      className={`flex items-center gap-1.5 hover:text-foreground transition-colors ${className || ""}`}
      onClick={() => column.toggleSorting(sorted === "asc")}
    >
      {children}
      <span className={`text-xs ${sorted ? "text-foreground" : "text-muted-foreground/50"}`}>
        {sorted === "asc" ? "▲" : sorted === "desc" ? "▼" : "▲▼"}
      </span>
    </button>
  )
}

function getAvgGrade(grades: TaskGradeInfo[]): number {
  const validGrades = grades.filter(g => g.grade !== null).map(g => g.grade as number)
  if (validGrades.length === 0) return -1
  return validGrades.reduce((sum, g) => sum + g, 0) / validGrades.length
}

export const repoColumns: ColumnDef<RepoSummary, any>[] = [
  {
    accessorKey: "name",
    header: ({ column }) => <SortableHeader column={column}>Repo</SortableHeader>,
    cell: ({ row }) => (
      <Link
        href={`/repo/${encodeURIComponent(row.original.name)}`}
        className="font-medium text-foreground hover:text-primary transition-colors"
      >
        {row.original.name}
      </Link>
    ),
  },
  {
    accessorKey: "lastRun",
    header: ({ column }) => <SortableHeader column={column}>Last Run</SortableHeader>,
    cell: ({ row }) => (
      <span className="text-muted-foreground">
        {formatRelativeTime(row.original.lastRun)}
      </span>
    ),
    sortingFn: (rowA, rowB) => {
      const a = rowA.original.lastRun ? new Date(rowA.original.lastRun).getTime() : 0
      const b = rowB.original.lastRun ? new Date(rowB.original.lastRun).getTime() : 0
      return a - b
    },
  },
  {
    id: "audit",
    accessorFn: (row) => getAvgGrade(row.taskGrades.audit),
    header: ({ column }) => (
      <SortableHeader column={column} className="justify-center w-full">Audit</SortableHeader>
    ),
    cell: ({ row }) => (
      <div className="flex justify-center items-start pt-2">
        <TaskGradeCell
          grades={row.original.taskGrades.audit}
          history={row.original.gradeHistory.audit}
          color={TASK_COLORS.audit}
          lastUpdated={row.original.taskLastUpdated.audit}
          rowId={row.id}
        />
      </div>
    ),
  },
  {
    id: "test",
    accessorFn: (row) => getAvgGrade(row.taskGrades.test),
    header: ({ column }) => (
      <SortableHeader column={column} className="justify-center w-full">Test</SortableHeader>
    ),
    cell: ({ row }) => (
      <div className="flex justify-center items-start pt-2">
        <TaskGradeCell
          grades={row.original.taskGrades.test}
          history={row.original.gradeHistory.test}
          color={TASK_COLORS.test}
          lastUpdated={row.original.taskLastUpdated.test}
          rowId={row.id}
        />
      </div>
    ),
  },
  {
    id: "fix",
    accessorFn: (row) => getAvgGrade(row.taskGrades.fix),
    header: ({ column }) => (
      <SortableHeader column={column} className="justify-center w-full">Fix</SortableHeader>
    ),
    cell: ({ row }) => (
      <div className="flex justify-center items-start pt-2">
        <TaskGradeCell
          grades={row.original.taskGrades.fix}
          history={row.original.gradeHistory.fix}
          color={TASK_COLORS.fix}
          lastUpdated={row.original.taskLastUpdated.fix}
          rowId={row.id}
        />
      </div>
    ),
  },
  {
    id: "refactor",
    accessorFn: (row) => getAvgGrade(row.taskGrades.refactor),
    header: ({ column }) => (
      <SortableHeader column={column} className="justify-center w-full">Refac</SortableHeader>
    ),
    cell: ({ row }) => (
      <div className="flex justify-center items-start pt-2">
        <TaskGradeCell
          grades={row.original.taskGrades.refactor}
          history={row.original.gradeHistory.refactor}
          color={TASK_COLORS.refactor}
          lastUpdated={row.original.taskLastUpdated.refactor}
          rowId={row.id}
        />
      </div>
    ),
  },
  {
    accessorKey: "pendingCount",
    header: ({ column }) => <SortableHeader column={column}>Pending</SortableHeader>,
    cell: ({ row }) => (
      <span className={row.original.pendingCount > 0 ? "text-amber-600 dark:text-amber-400 font-medium" : "text-muted-foreground"}>
        {row.original.pendingCount}
      </span>
    ),
  },
  {
    accessorKey: "reportCount",
    header: ({ column }) => <SortableHeader column={column}>Reports</SortableHeader>,
    cell: ({ row }) => (
      <Link
        href={`/repo/${encodeURIComponent(row.original.name)}`}
        className="text-muted-foreground hover:text-foreground hover:underline transition-colors"
      >
        {row.original.reportCount}
      </Link>
    ),
  },
]
