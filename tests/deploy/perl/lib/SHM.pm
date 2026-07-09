package SHM;

use strict;
use warnings;

use Exporter qw(import);

our @EXPORT = qw(get_service);
our @EXPORT_OK = @EXPORT;
our %EXPORT_TAGS = (
    all => [ @EXPORT ],
);

our $initialized             = 0;
our $commit_count            = 0;
our $init_before_get_service = 0;
our $STATE_FILE;
our @EVENT_LOG;

our %PAY_SYSTEMS_DATA = (
    srv_customlab_nalog => {
        enabled        => 1,
        version        => '1.2.3',
        need_update_to => '2.0.0',
        client_token   => 'secret-token-must-not-appear-in-status',
        service_name   => 'vff-fiscal',
        pay_systems    => ['card'],
    },
);

sub reset_state {
    $initialized             = 0;
    $commit_count            = 0;
    $init_before_get_service = 0;
    %PAY_SYSTEMS_DATA = (
        srv_customlab_nalog => {
            enabled        => 1,
            version        => '1.2.3',
            need_update_to => '2.0.0',
            client_token   => 'secret-token-must-not-appear-in-status',
            service_name   => 'vff-fiscal',
            pay_systems    => ['card'],
        },
    );
    $STATE_FILE = undef;
    @EVENT_LOG = ();
}

sub _record_event {
    my ($event) = @_;
    push @EVENT_LOG, $event;
    return unless defined $STATE_FILE && length $STATE_FILE;

    if ( open my $fh, '>>', $STATE_FILE ) {
        print {$fh} "$event\n";
        close $fh;
    }
}

sub read_recorded_events {
    my ($path) = @_;
    return @EVENT_LOG unless defined $path && length $path && -f $path;

    open my $fh, '<', $path or return;
    my @events = readline $fh;
    chomp @events;
    close $fh;
    return @events;
}

sub new {
    my ($class, %opts) = @_;
    $initialized = 1;
    _record_event('shm_initialized');
    return bless { %opts }, $class;
}

sub commit {
    my ($self) = @_;
    die "commit called without SHM initialization\n" unless $initialized;
    $commit_count++;
    _record_event('commit');
    return $self;
}

sub get_service {
    $init_before_get_service = $initialized ? 1 : 0;
    _record_event( 'get_service init=' . ( $initialized ? 1 : 0 ) );
    die "get_service called before SHM initialization\n" unless $initialized;

    my ($name, %args) = @_;
    return unless $name eq 'config' && ( $args{_id} // '' ) eq 'pay_systems';

    return bless {}, 'SHM::MockConfigService';
}

{
    package SHM::MockConfigService;

    sub get_data {
        if ( my $stored = $Core::Config::STORE{pay_systems} ) {
            return _copy_pay_systems($stored);
        }
        return { %SHM::PAY_SYSTEMS_DATA };
    }

    sub _copy_pay_systems {
        my ($value) = @_;
        my %copy;
        for my $key ( keys %{$value} ) {
            my $entry = $value->{$key};
            $copy{$key} = ref $entry eq 'HASH' ? { %{$entry} } : $entry;
        }
        return \%copy;
    }
}

$STATE_FILE = $ENV{SHM_TEST_STATE_FILE}
    if defined $ENV{SHM_TEST_STATE_FILE} && length $ENV{SHM_TEST_STATE_FILE};

1;
