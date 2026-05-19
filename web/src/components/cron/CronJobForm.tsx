import { useMemo, useState, useEffect } from 'react';
import { Save, X, Power, PowerOff } from 'lucide-react';
import ChannelPicker from '../channels/ChannelPicker';
import type { CronJob } from '../../types';
import {
  SCHEDULE_PRESETS,
  ADVANCED_SCHEDULE_VALUE,
  matchesPreset,
  MODE_OPTIONS,
  validateCronExpression,
  nextRun,
  formatRelativeTime,
  EMPTY_CRON_FORM,
  formStateFromJob,
  type CronJobFormState,
} from './cronJobHelpers';

interface CronJobFormProps {
  isCreating: boolean;
  initial?: CronJob | null;
  onSave: (form: CronJobFormState) => Promise<void> | void;
  onCancel: () => void;
}

export default function CronJobForm({ isCreating, initial, onSave, onCancel }: CronJobFormProps) {
  const [form, setForm] = useState<CronJobFormState>(() =>
    initial ? formStateFromJob(initial) : EMPTY_CRON_FORM,
  );
  const [scheduleMode, setScheduleMode] = useState<string>(() =>
    initial ? matchesPreset(initial.schedule) : '*/15 * * * *',
  );
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    if (initial) {
      setForm(formStateFromJob(initial));
      setScheduleMode(matchesPreset(initial.schedule));
    }
  }, [initial]);

  const validation = useMemo(() => validateCronExpression(form.schedule), [form.schedule]);
  const nextRunPreview = useMemo(() => {
    if (!validation.valid) return null;
    return nextRun(form.schedule);
  }, [form.schedule, validation.valid]);

  const onSchedulePresetChange = (value: string) => {
    setScheduleMode(value);
    if (value !== ADVANCED_SCHEDULE_VALUE) {
      setForm((f) => ({ ...f, schedule: value }));
    }
  };

  const submit = async () => {
    setError(null);
    if (!form.name.trim()) {
      setError('Name is required');
      return;
    }
    if (!form.prompt.trim()) {
      setError('Prompt is required');
      return;
    }
    if (!validation.valid) {
      setError(validation.message ?? 'Schedule is invalid');
      return;
    }
    try {
      setSubmitting(true);
      await onSave(form);
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to save cron job');
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <div className="p-6 bg-gray-50 dark:bg-gray-900/50 rounded-lg border border-gray-200 dark:border-gray-700 animate-fade-in">
      <h3 className="text-lg font-semibold text-gray-900 dark:text-white mb-6">
        {isCreating ? 'Create Cron Job' : 'Edit Cron Job'}
      </h3>

      <div className="space-y-6">
        <div>
          <label className="block text-sm font-medium text-gray-700 dark:text-gray-300 mb-1">
            Name <span className="text-red-500">*</span>
          </label>
          <input
            type="text"
            className="input-field"
            placeholder="e.g., Morning incident digest"
            value={form.name}
            onChange={(e) => setForm({ ...form, name: e.target.value })}
          />
        </div>

        <div>
          <label className="block text-sm font-medium text-gray-700 dark:text-gray-300 mb-1">
            Description
          </label>
          <input
            type="text"
            className="input-field"
            placeholder="Optional — shown in the cron list"
            value={form.description}
            onChange={(e) => setForm({ ...form, description: e.target.value })}
          />
        </div>

        <div>
          <label className="block text-sm font-medium text-gray-700 dark:text-gray-300 mb-1">
            Schedule <span className="text-red-500">*</span>
          </label>
          <select
            className="input-field mb-2"
            value={scheduleMode}
            onChange={(e) => onSchedulePresetChange(e.target.value)}
          >
            {SCHEDULE_PRESETS.map((p) => (
              <option key={p.value} value={p.value}>
                {p.label} — {p.value}
              </option>
            ))}
            <option value={ADVANCED_SCHEDULE_VALUE}>Advanced (raw cron expression)</option>
          </select>
          {scheduleMode === ADVANCED_SCHEDULE_VALUE && (
            <input
              type="text"
              className="input-field font-mono"
              placeholder="*/5 * * * *"
              value={form.schedule}
              onChange={(e) => setForm({ ...form, schedule: e.target.value })}
              aria-label="Cron expression"
            />
          )}
          {!validation.valid && (
            <p className="mt-1 text-xs text-red-600 dark:text-red-400">{validation.message}</p>
          )}
          {validation.valid && nextRunPreview && (
            <p className="mt-1 text-xs text-gray-500 dark:text-gray-400">
              Next run {formatRelativeTime(nextRunPreview)} ({nextRunPreview.toLocaleString()})
            </p>
          )}
        </div>

        <div>
          <label className="block text-sm font-medium text-gray-700 dark:text-gray-300 mb-1">
            Prompt <span className="text-red-500">*</span>
          </label>
          <textarea
            className="input-field min-h-[120px] font-mono"
            placeholder="What should the cron ask?  e.g. 'List incidents opened in the last 24 hours and summarise root causes'"
            value={form.prompt}
            onChange={(e) => setForm({ ...form, prompt: e.target.value })}
          />
          <p className="mt-1 text-xs text-gray-500 dark:text-gray-400">
            One-shot mode: prompt is sent as a single LLM completion. Agent mode: prompt is the
            initial task handed to the incident-manager skill.
          </p>
        </div>

        <div>
          <label className="block text-sm font-medium text-gray-700 dark:text-gray-300 mb-2">
            Mode <span className="text-red-500">*</span>
          </label>
          <div className="space-y-2">
            {MODE_OPTIONS.map((opt) => (
              <label
                key={opt.value}
                className={`flex items-start gap-3 p-3 rounded-lg border cursor-pointer transition-colors ${
                  form.mode === opt.value
                    ? 'border-primary-500 bg-primary-50 dark:bg-primary-900/20'
                    : 'border-gray-200 dark:border-gray-700 bg-white dark:bg-gray-800'
                }`}
              >
                <input
                  type="radio"
                  name="cron-mode"
                  value={opt.value}
                  checked={form.mode === opt.value}
                  onChange={() => setForm({ ...form, mode: opt.value })}
                  className="mt-1"
                />
                <div>
                  <div className="text-sm font-medium text-gray-900 dark:text-white">
                    {opt.label}
                  </div>
                  <div className="text-xs text-gray-500 dark:text-gray-400">{opt.description}</div>
                </div>
              </label>
            ))}
          </div>
        </div>

        <ChannelPicker
          label="Post results to channel"
          value={form.channel_uuid}
          onChange={(uuid) => setForm({ ...form, channel_uuid: uuid })}
        />

        <div className="flex items-center gap-3 p-4 rounded-lg bg-white dark:bg-gray-800 border border-gray-200 dark:border-gray-700">
          <input
            type="checkbox"
            id="cron-enabled"
            checked={form.enabled}
            onChange={(e) => setForm({ ...form, enabled: e.target.checked })}
          />
          <label htmlFor="cron-enabled" className="flex items-center gap-2 cursor-pointer">
            {form.enabled ? (
              <Power className="w-4 h-4 text-green-500" />
            ) : (
              <PowerOff className="w-4 h-4 text-gray-400" />
            )}
            <span className="text-sm text-gray-700 dark:text-gray-300">
              {form.enabled ? 'Enabled' : 'Disabled (will not fire)'}
            </span>
          </label>
        </div>

        {error && (
          <div className="text-sm text-red-600 dark:text-red-400 bg-red-50 dark:bg-red-900/20 border border-red-200 dark:border-red-800 p-3 rounded">
            {error}
          </div>
        )}

        <div className="flex gap-3 pt-4 border-t border-gray-200 dark:border-gray-700">
          <button
            onClick={submit}
            className="btn btn-primary"
            disabled={submitting || !validation.valid}
          >
            <Save className="w-4 h-4" />
            {submitting ? 'Saving…' : 'Save'}
          </button>
          <button onClick={onCancel} className="btn btn-secondary" disabled={submitting}>
            <X className="w-4 h-4" />
            Cancel
          </button>
        </div>
      </div>
    </div>
  );
}
