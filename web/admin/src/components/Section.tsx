/**
 * 分区容器：统一「区块标题 + 内容 / 降级提示」的外观。
 *
 * 单独抽出来的理由：快照的九个分区**各自独立降级**（admin.Snapshot.Errors
 * 按分区名索引）。如果每块自己写一套降级 UI，很快就会出现「有的块白屏、
 * 有的块显示 0」这类不一致 —— 而「分区失败」与「分区是空数据」在运维眼里
 * 是完全不同的两件事，必须长得不一样。
 */
import type { ReactNode } from 'react'

interface Props {
  title: string
  /** 该分区的失败原因；有值即渲染降级态而不是 children。 */
  error?: string
  /** 分区内二级降级（如 agents 里的监控摘要失败，但状态分布仍然有效）。 */
  warning?: string
  children: ReactNode
}

export default function Section({ title, error, warning, children }: Props) {
  return (
    <section className="section">
      <h2>{title}</h2>
      {error ? (
        <p className="section-error">该分区取数失败：{error}</p>
      ) : (
        <>
          {warning ? <p className="section-warning">{warning}</p> : null}
          {children}
        </>
      )}
    </section>
  )
}
