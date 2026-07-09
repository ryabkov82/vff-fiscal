#!/usr/bin/env perl
use strict;
use warnings;

use FindBin qw($Bin);
use File::Spec;
use File::Temp qw(tempfile);
use Test::More;
use JSON::PP ();

use lib File::Spec->catdir( $Bin, '..', 'perl', 'lib' );
use SHM ();
use Core::Config ();

my $REPO_ROOT = File::Spec->catdir( $Bin, '..', '..', '..' );
my $SCRIPT    = File::Spec->catfile(
    $REPO_ROOT,
    'ansible/roles/vff_fiscal_adapter/files/shm-config.pl'
);
my $PERL5LIB = join ':',
    File::Spec->catdir( $Bin, '..', 'perl', 'lib' ),
    File::Spec->catdir( $REPO_ROOT, 'adapters/shm/lib' );

ok( -f $SCRIPT, 'shm-config.pl exists for behavioral testing' );

sub reset_stub_state {
    SHM::reset_state();
    Core::Config::reset_state();
}

sub run_shm_config {
    my (@args) = @_;
    reset_stub_state();

    my ( $state_fh, $state_file ) = tempfile( UNLINK => 1 );
    close $state_fh;

    local $ENV{PERL5LIB}            = $PERL5LIB;
    local $ENV{SHM_TEST_STATE_FILE} = $state_file;

    my $output = `"$^X" "$SCRIPT" @args 2>&1`;
    my $exit   = $? >> 8;
    my @events = SHM::read_recorded_events($state_file);
    return ( $output, $exit, \@events );
}

sub decode_json_output {
    my ($output) = @_;
    my ($json_line) = $output =~ /\A(\{.*\})\s*\z/s;
    ok( defined $json_line, 'command emitted JSON output' )
        or diag("raw output: $output");
    return defined $json_line ? JSON::PP::decode_json($json_line) : undef;
}

sub assert_shm_initialized_before_get_service {
    my ($events) = @_;
    my ( $init_index, $get_service_index );

    for my $idx ( 0 .. $#{$events} ) {
        $init_index = $idx if $events->[$idx] eq 'shm_initialized';
        if ( $events->[$idx] =~ /\Aget_service init=1\z/ ) {
            $get_service_index = $idx;
            last;
        }
    }

    ok( defined $init_index, 'subprocess initialized SHM' );
    ok( defined $get_service_index, 'subprocess reached get_service with initialized SHM' );
    ok(
        defined $init_index
            && defined $get_service_index
            && $init_index < $get_service_index,
        'SHM initialization happened before get_service'
    );
}

sub assert_commit_after_init {
    my ($events) = @_;
    my ( $init_index, $commit_index );

    for my $idx ( 0 .. $#{$events} ) {
        $init_index   = $idx if $events->[$idx] eq 'shm_initialized';
        $commit_index = $idx if $events->[$idx] eq 'commit';
    }

    ok( defined $init_index, 'subprocess initialized SHM before commit' );
    ok( defined $commit_index, 'subprocess committed through SHM' );
    ok(
        defined $init_index
            && defined $commit_index
            && $init_index < $commit_index,
        'commit happened after SHM initialization'
    );
}

subtest 'get_service fails without SHM initialization' => sub {
    reset_stub_state();

    my $failed = eval { SHM::get_service( 'config', _id => 'pay_systems' ); 1 };
    ok( !$failed, 'get_service dies when SHM is not initialized' );
    like( $@, qr/get_service called before SHM initialization/, 'failure message is explicit' );
    is( $SHM::init_before_get_service, 0, 'SHM was not initialized at get_service time' );
};

subtest 'status initializes SHM before get_service' => sub {
    my ( $output, $exit, $events ) = run_shm_config('status');
    is( $exit, 0, 'status exits successfully' );

    assert_shm_initialized_before_get_service($events);

    my $payload = decode_json_output($output);
    ok( $payload, 'status returns valid JSON' ) or return;

    is( $payload->{version}, '1.2.3', 'status reports module version' );
    ok( $payload->{enabled}, 'status reports enabled flag' );
    ok( $payload->{client_token_present}, 'status reports client_token presence without exposing value' );
    ok( !exists $payload->{client_token}, 'status output does not include client_token' );
    unlike( $output, qr/secret-token-must-not-appear-in-status/, 'status output does not leak client_token value' );
};

subtest 'clear-update-marker uses initialized SHM context and commits' => sub {
    my ( $output, $exit, $events ) = run_shm_config('clear-update-marker');
    is( $exit, 0, 'clear-update-marker exits successfully' );

    assert_shm_initialized_before_get_service($events);
    assert_commit_after_init($events);

    my $payload = decode_json_output($output);
    ok( $payload, 'clear-update-marker returns valid JSON' ) or return;
    is( $payload->{status}, 200, 'clear-update-marker reports success' );
    ok( !$payload->{need_update_to_defined}, 'need_update_to is cleared in response' );
};

subtest 'set-enabled uses initialized SHM context and commits' => sub {
    my ( $output, $exit, $events ) = run_shm_config( 'set-enabled', '0' );
    is( $exit, 0, 'set-enabled exits successfully' );

    assert_shm_initialized_before_get_service($events);
    assert_commit_after_init($events);

    my $payload = decode_json_output($output);
    ok( $payload, 'set-enabled returns valid JSON' ) or return;
    is( $payload->{status}, 200, 'set-enabled reports success' );
    ok( !$payload->{enabled}, 'enabled flag was updated' );
    ok( !$payload->{need_update_to_defined}, 'need_update_to remains cleared' );
};

done_testing();
