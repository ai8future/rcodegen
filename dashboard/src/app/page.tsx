'use client'

import { useEffect, useState } from 'react'
import Link from 'next/link'
import { DataTable } from '@/components/ui/data-table'
import { repoColumns, ExpandedGraphsPanel, RepoSummary } from '@/components/repos-columns'

interface DaemonStatus {
  running: boolean
  lastHeartbeat: string | null
}

function DaemonIndicator({ status }: { status: DaemonStatus | null }) {
  if (!status) {
    return (
      <div className="flex items-center gap-2 text-muted-foreground">
        <div className="w-2 h-2 rounded-full bg-muted-foreground" />
        <span className="text-sm">Loading...</span>
      </div>
    )
  }

  if (status.running) {
    return (
      <div className="flex items-center gap-2 text-green-600 dark:text-green-400">
        <div className="w-2 h-2 rounded-full bg-green-500 animate-pulse" />
        <span className="text-sm">Scheduler Running</span>
      </div>
    )
  }

  return (
    <div className="flex items-center gap-2 text-muted-foreground">
      <div className="w-2 h-2 rounded-full bg-muted-foreground" />
      <span className="text-sm">Scheduler Stopped</span>
    </div>
  )
}

export default function Home() {
  const [repos, setRepos] = useState<RepoSummary[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [daemonStatus, setDaemonStatus] = useState<DaemonStatus | null>(null)
  const [lastUpdated, setLastUpdated] = useState<Date | null>(null)

  useEffect(() => {
    const fetchData = () => {
      fetch('/api/repos')
        .then(res => res.json())
        .then(data => {
          if (data.error) {
            setError(data.error)
          } else {
            setRepos(data)
            setError(null)
            setLastUpdated(new Date())
          }
          setLoading(false)
        })
        .catch(err => {
          setError(err.message)
          setLoading(false)
        })

      fetch('/api/daemon/status')
        .then(res => res.json())
        .then(data => setDaemonStatus(data))
        .catch(() => setDaemonStatus({ running: false, lastHeartbeat: null }))
    }

    fetchData()
    const interval = setInterval(fetchData, 60000) // Refresh every minute
    return () => clearInterval(interval)
  }, [])

  return (
    <div className="min-h-screen bg-background">
      <header className="border-b border-border bg-card">
        <div className="max-w-6xl mx-auto px-6 py-4 flex items-center justify-between">
          <h1 className="text-xl font-semibold text-foreground">
            rcodegen Dashboard
          </h1>
          <div className="flex items-center gap-6">
            <DaemonIndicator status={daemonStatus} />
            <Link
              href="/schedules"
              className="text-sm text-muted-foreground hover:text-foreground transition-colors"
            >
              Schedules
            </Link>
          </div>
        </div>
      </header>

      <main className="max-w-6xl mx-auto px-6 py-8">
        {loading ? (
          <div className="text-center py-12 text-muted-foreground">Loading repos...</div>
        ) : error ? (
          <div className="text-center py-12 text-destructive">{error}</div>
        ) : repos.length === 0 ? (
          <div className="text-center py-12 text-muted-foreground">
            No repos with rcodegen reports found
          </div>
        ) : (
          <>
            <DataTable
              columns={repoColumns}
              data={repos}
              renderExpandedRow={(row, onCollapse) => (
                <ExpandedGraphsPanel
                  gradeHistory={row.original.gradeHistory}
                  onClose={onCollapse}
                />
              )}
            />
            {lastUpdated && (
              <div className="mt-4 text-xs text-muted-foreground text-center">
                Auto-refreshes every minute. Last updated: {lastUpdated.toLocaleTimeString()}
              </div>
            )}
          </>
        )}
      </main>
    </div>
  )
}
