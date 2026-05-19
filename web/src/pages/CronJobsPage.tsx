import PageHeader from '../components/PageHeader';
import CronJobsManager from '../components/cron/CronJobsManager';

export default function CronJobsPage() {
  return (
    <div className="animate-fade-in max-w-6xl mx-auto">
      <PageHeader
        title="Cron Jobs"
        description="Recurring schedules that fire either a one-shot LLM completion or a full agent investigation, and post results to a Channel."
      />
      <CronJobsManager />
    </div>
  );
}
